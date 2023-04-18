package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	log "github.com/sirupsen/logrus"
	"github.com/valyala/fasthttp"
	"github.com/valyala/fasthttp/fasthttpproxy"
)

const (
	AWS_ECR_ENDPOINT_URI            = "https://api.us-east-1.gallery.ecr.aws"
	AWS_ECR_SEARCH_REQUEST_URI      = "/searchRepositoryCatalogData"
	AWS_ECR_DESCRIBE_IMAGE_TAGS_URI = "/describeImageTags"
	AWS_ECR_SEARCH_REQUEST_METHOD   = "POST"
	SEARCH_KEYSPACE                 = "abcdefghijklmnopqrstuvwxyz"
	REDIS_EXPORT_BATCH_SIZE         = 100
	REDIS_FLATTEN_THREAD_COUNT      = 2
	REDIS_EXPORT_THREAD_COUNT       = 4
)

var (
	AWS_ECR_SEARCH_REQUEST_HEADERS = map[string]string{
		"Content-Type": "application/json",
		"User-Agent":   "Mozilla/5.0 (X11; Linux x86_64; rv:102.0) Gecko/20100101 Firefox/102.0",
		"Referer":      "https://gallery.ecr.aws/",
		"Origin":       "https://gallery.ecr.aws",
	}

	MAX_CRAWL_DEPTH = 8

	searchRequestPool = sync.Pool{
		New: func() interface{} {
			req := &fasthttp.Request{}
			req.Header.SetMethod(AWS_ECR_SEARCH_REQUEST_METHOD)
			for key, value := range AWS_ECR_SEARCH_REQUEST_HEADERS {
				req.Header.Set(key, value)
			}
			return req
		},
	}
	httpClientPool = sync.Pool{
		New: func() interface{} {
			return &fasthttp.Client{
				Dial: fasthttpproxy.FasthttpProxyHTTPDialer(),
				TLSConfig: &tls.Config{
					InsecureSkipVerify: true,
				},
			}
		},
	}

	imageMapLock = &sync.RWMutex{}
	imageMap     map[string]map[string]bool

	statsLock           = &sync.RWMutex{}
	activeGoroutines    = 0
	foundImageCount     = 0
	imageWithTagCount   = 0
	totalTagsFoundCount = 0
)

type AwsEcrCrawlInput struct {
	SearchTerm   string
	CurrentLevel int
}

func parseAwsEcrResponse(searchInput *AwsEcrCrawlInput, resp *AwsEcrSearchResponse, imageChan chan *EcrRepositoryInfo) {
	imageMapLock.Lock()
	for _, image := range resp.RepositoryCatalogSearchResultList {
		if _, exists := imageMap[image.PrimaryRegistryAliasName]; exists {
			if _, exists := imageMap[image.PrimaryRegistryAliasName][image.RepositoryName]; !exists {
				imageMap[image.PrimaryRegistryAliasName][image.RepositoryName] = true
				imageChan <- image
				statsLock.Lock()
				foundImageCount += 1
				statsLock.Unlock()
				log.WithFields(log.Fields{"search-term": searchInput.SearchTerm, "state": "deepscan", "registry-alias": image.PrimaryRegistryAliasName, "repo-name": image.RepositoryName}).Tracef("added image to output")
			}
		} else {
			imageMap[image.PrimaryRegistryAliasName] = make(map[string]bool)
			imageMap[image.PrimaryRegistryAliasName][image.RepositoryName] = true
			imageChan <- image
			statsLock.Lock()
			foundImageCount += 1
			statsLock.Unlock()
			log.WithFields(log.Fields{"search-term": searchInput.SearchTerm, "state": "deepscan", "registry-alias": image.PrimaryRegistryAliasName, "repo-name": image.RepositoryName}).Tracef("added image to output")
		}
	}
	imageMapLock.Unlock()
}

func getEcrImagesFromSearchTerm(searchInput *AwsEcrCrawlInput, imageChan chan *EcrRepositoryInfo, wg *sync.WaitGroup) {
	defer wg.Done()
	nextToken := ""
	statsLock.Lock()
	activeGoroutines += 1
	statsLock.Unlock()
	log.WithFields(log.Fields{"search-term": searchInput.SearchTerm, "state": "scan", "current-depth": searchInput.CurrentLevel}).Tracef("goroutine started")
	for {
		resp, err := makeEcrSearchRequest(searchInput.SearchTerm, nextToken)
		if err != nil {
			log.WithFields(log.Fields{"errormsg": err.Error(), "search-term": searchInput.SearchTerm, "next-token": nextToken, "state": "scan"}).Errorf("error getting initial response & count from the server")
			break
		}
		searchTotalResults := resp.TotalResults
		if searchTotalResults > 2500 {
			if searchInput.CurrentLevel < MAX_CRAWL_DEPTH {
				log.WithFields(log.Fields{"search-term": searchInput.SearchTerm, "image-count": searchTotalResults, "state": "scan", "current-depth": searchInput.CurrentLevel}).Debugf("forking")
				nextSearchChars := strings.Split(SEARCH_KEYSPACE, "")
				childWg := &sync.WaitGroup{}
				childWg.Add(len(nextSearchChars))
				for _, char := range nextSearchChars {
					nextSearchTerm := searchInput.SearchTerm + char
					nextSearchInputs := &AwsEcrCrawlInput{
						SearchTerm:   nextSearchTerm,
						CurrentLevel: searchInput.CurrentLevel + 1,
					}
					go getEcrImagesFromSearchTerm(nextSearchInputs, imageChan, childWg)
				}
				childWg.Wait()
				break
			} else {
				log.WithFields(log.Fields{"search-term": searchInput.SearchTerm, "image-count": searchTotalResults, "state": "scan", "current-depth": searchInput.CurrentLevel}).Errorf("reached max depth. returning")
				break
			}
		} else {
			if searchTotalResults == 0 {
				log.WithFields(log.Fields{"search-term": searchInput.SearchTerm, "image-count": 0, "state": "scan"}).Trace("returning")
				break
			}
			log.WithFields(log.Fields{"search-term": searchInput.SearchTerm, "image-count": searchTotalResults, "state": "scan"}).Debugf("running deepscan")
			parseAwsEcrResponse(searchInput, resp, imageChan)
			nextToken = resp.NextToken
			if nextToken == "" {
				log.WithFields(log.Fields{"search-term": searchInput.SearchTerm, "state": "scan", "current-depth": searchInput.CurrentLevel}).Tracef("done scanning all images. returning")
				break
			}
		}
	}
	log.WithFields(log.Fields{"search-term": searchInput.SearchTerm, "state": "deepscan", "current-depth": searchInput.CurrentLevel}).Debugf("done finding all relevant images")
	statsLock.Lock()
	activeGoroutines -= 1
	statsLock.Unlock()
}

func getAllEcrImages(threadCount int, imageChan chan *EcrRepositoryInfo) error {
	inputChan := make(chan *AwsEcrCrawlInput, 5*threadCount)
	go func() {
		log.WithFields(log.Fields{"state": "input"}).Infof("starting input generation")
		for _, char1 := range strings.Split(SEARCH_KEYSPACE, "") {
			for _, char2 := range strings.Split(SEARCH_KEYSPACE, "") {
				for _, char3 := range strings.Split(SEARCH_KEYSPACE, "") {
					for _, char4 := range strings.Split(SEARCH_KEYSPACE, "") {
						inputString := char1 + char2 + char3 + char4
						inputChan <- &AwsEcrCrawlInput{
							SearchTerm:   inputString,
							CurrentLevel: 4,
						}
						log.WithFields(log.Fields{"search-term": inputString, "state": "input"}).Tracef("added to the input chan")
					}
				}
			}
		}
		log.WithFields(log.Fields{"state": "input"}).Infof("done adding all inputs to searchspace")
		close(inputChan)
	}()

	parentWg := &sync.WaitGroup{}
	log.WithFields(log.Fields{"state": "main"}).Infof("starting all threads for input processing")
	for i := 0; i < threadCount; i++ {
		parentWg.Add(1)
		go func(i int) {
			log.WithFields(log.Fields{"state": "main", "thread-id": i}).Tracef("starting thread")
			for searchInput := range inputChan {
				childWg := &sync.WaitGroup{}
				childWg.Add(1)
				log.WithFields(log.Fields{"state": "main", "thread-id": i, "search-term": searchInput.SearchTerm}).Tracef("thread started scanning")
				getEcrImagesFromSearchTerm(searchInput, imageChan, childWg)
				childWg.Wait()
				log.WithFields(log.Fields{"state": "main", "thread-id": i, "search-term": searchInput.SearchTerm}).Tracef("thread finished scanning")
			}
			log.WithFields(log.Fields{"state": "main", "thread-id": i}).Trace("thread exited!")
			parentWg.Done()
		}(i)
	}

	log.WithFields(log.Fields{"state": "main"}).Infof("waiting for threads to finish")
	parentWg.Wait()
	log.WithFields(log.Fields{"state": "main"}).Infof("threads finished scanning!")
	close(imageChan)
	return nil
}

func getImageTags(imageChan chan *EcrRepositoryInfo, rawResultsChan chan *EcrResult, wg *sync.WaitGroup) {
	defer wg.Done()
	for image := range imageChan {
		resp, err := describeImageTags(image)
		if err != nil {
			log.WithFields(log.Fields{"state": "tags", "errormsg": err.Error(), "registry": image.PrimaryRegistryAliasName, "repository": image.RepositoryName}).Tracef("error getting image tags")
			continue
		}
		tagsFound := len(*resp.ImageTagDetails)
		if tagsFound > 0 {
			log.WithFields(log.Fields{"state": "tags", "registry": image.PrimaryRegistryAliasName, "repository": image.RepositoryName, "tag-count": tagsFound}).Tracef("found tags!")
			result := &EcrResult{
				RepositoryInfo: image,
				ImageTags:      resp,
			}
			rawResultsChan <- result
			statsLock.Lock()
			imageWithTagCount += 1
			totalTagsFoundCount += tagsFound
			statsLock.Unlock()
		}
		time.Sleep(time.Second)
	}

}

func sendResultsToRedis(ctx context.Context, results chan *EcrResult, redisKeyPrefix int, wg *sync.WaitGroup, overwrite bool) error {
	defer wg.Done()
	redisHost, exists := os.LookupEnv("REDIS_HOST")
	if !exists {
		log.WithFields(log.Fields{"state": "redis", "errmsg": "REDIS_HOST not defined"}).Fatal("error getting redis connection configuration")
	}
	redisPassword, exists := os.LookupEnv("REDIS_PASSWORD")
	if !exists {
		log.WithFields(log.Fields{"state": "redis", "errmsg": "REDIS_PASSWORD not defined"}).Fatal("error getting redis connection configuration")
	}
	rdb := redis.NewClient(&redis.Options{Addr: redisHost, Password: redisPassword, DB: 1})

	flattenedResults := make(chan redis.Z, 2000)

	// flatten the results
	log.WithFields(log.Fields{"state": "redis"}).Infof("creating the redis result set")
	flattenWg := &sync.WaitGroup{}
	flattenWg.Add(REDIS_FLATTEN_THREAD_COUNT)
	for i := 0; i < REDIS_FLATTEN_THREAD_COUNT; i++ {
		go flattenEcrResults(results, flattenedResults, flattenWg)
	}

	// build result set
	currentSetKey := fmt.Sprintf("ecr-scan:%d", redisKeyPrefix)
	log.WithFields(log.Fields{"state": "redis"}).Infof("result set key: %v", currentSetKey)

	if resultCount, err := rdb.Exists(ctx, currentSetKey).Result(); err != nil {
		log.WithFields(log.Fields{"state": "redis", "action": "store", "errmsg": err.Error()}).Fatalf("error connecting to redis instance")
	} else {
		if resultCount != 0 {
			if overwrite {
				log.WithFields(log.Fields{"state": "redis", "action": "store"}).Infof("key [ %v ] will be recreated in redis", currentSetKey)
				rdb.Del(ctx, currentSetKey)
			} else {
				log.WithFields(log.Fields{"state": "redis", "action": "store", "errmsg": "key exists exception"}).Fatalf("key [ %v ] already found in redis. overwrite flag not supplied! exiting. ", currentSetKey)
			}
		}
	}

	resultSetWg := &sync.WaitGroup{}
	resultSetWg.Add(REDIS_EXPORT_THREAD_COUNT)
	for i := 0; i < REDIS_EXPORT_THREAD_COUNT; i++ {
		go redisBuildResultSet(rdb, ctx, flattenedResults, currentSetKey, resultSetWg)
	}

	// wait for flattening to complete
	flattenWg.Wait()
	close(flattenedResults)
	log.WithFields(log.Fields{"state": "redis"}).Debugf("result set data mapping completed")

	// wait for redis result sets storing to complete
	resultSetWg.Wait()
	log.WithFields(log.Fields{"state": "redis"}).Infof("done storing result set in redis")

	previousSetKey := fmt.Sprintf("ecr-scan:%d", redisKeyPrefix-1)

	diffSetKey := fmt.Sprintf("ecr-diff:%d", redisKeyPrefix)

	newFindingsCount := 0

	if exists, _ := rdb.Exists(ctx, previousSetKey).Result(); exists == 0 {
		log.WithFields(log.Fields{"state": "redis", "action": "diff"}).Infof("key [ %v ] not found in redis. not generating diff", previousSetKey)
	} else {
		resp, err := rdb.ZDiffStore(ctx, diffSetKey, currentSetKey, previousSetKey).Result()
		if err != nil {
			log.WithFields(log.Fields{"state": "redis", "action": "diff", "errmsg": err.Error()}).Error("error generating diff between sets")
		}
		newFindingsCount = int(resp)
		log.WithFields(log.Fields{"state": "redis", "action": "diff", "count": resp}).Infof("generated diff in redis")
	}

	log.WithFields(log.Fields{"state": "redis", "action": "metadata"}).Infof("attempting to storing metadata")
	statsLock.Lock()
	value := &RedisMetaDataValue{
		TotalImages:   imageWithTagCount,
		TotalFindings: totalTagsFoundCount,
		NewFindings:   newFindingsCount,
		DateTime:      time.Now().Format("01-02-2006 15:04:05"),
		SetKey:        currentSetKey,
	}
	statsLock.Unlock()

	data, err := json.Marshal(value)
	if err != nil {
		log.WithFields(log.Fields{"state": "redis", "action": "metadata", "errmsg": err.Error()}).Errorf("error marshalling metadata object")
	} else {
		log.Info(string(data))
		rdb.LPush(ctx, "ecr-scanner-metadata", string(data))
		log.WithFields(log.Fields{"state": "redis", "action": "metadata"}).Infof("done saving metadata")
	}

	return nil
}

func redisBuildResultSet(rdb *redis.Client, ctx context.Context, results chan redis.Z, currentSetKey string, wg *sync.WaitGroup) error {
	defer wg.Done()
	var nextFindings []redis.Z
	index := 0
	for result := range results {
		if index == REDIS_EXPORT_BATCH_SIZE {
			if resp := rdb.ZAdd(ctx, currentSetKey, nextFindings...); resp.Err() != nil {
				log.WithFields(log.Fields{"state": "redis", "action": "store", "errmsg": resp.Err()}).Error("error saving results in redis!")
				return resp.Err()
			}
			log.WithFields(log.Fields{"state": "redis", "action": "store"}).Debugf("pushed batch results to redis")
			index = 0
		}
		if index == 0 {
			nextFindings = make([]redis.Z, REDIS_EXPORT_BATCH_SIZE)
		}
		nextFindings[index] = result
		index += 1
	}
	// flush out anything left after channel is closed
	if resp := rdb.ZAdd(ctx, currentSetKey, nextFindings...); resp.Err() != nil {
		log.WithFields(log.Fields{"state": "redis", "action": "store", "errmsg": resp.Err()}).Error("error saving results in redis!")
		return resp.Err()
	}
	log.WithFields(log.Fields{"state": "redis", "action": "store"}).Debugf("pushed final batch of results to redis")
	return nil
}

func flattenEcrResults(results chan *EcrResult, flattednedResults chan redis.Z, wg *sync.WaitGroup) {
	defer wg.Done()
	for result := range results {
		for _, tagDetails := range *result.ImageTags.ImageTagDetails {
			redisValue := fmt.Sprintf("%v/%v:%v", result.RepositoryInfo.PrimaryRegistryAliasName, result.RepositoryInfo.RepositoryName, tagDetails.ImageTag)
			flattednedResults <- redis.Z{
				Member: redisValue,
				Score:  float64(result.RepositoryInfo.DownloadCount),
			}
		}
	}
}

func main() {
	threadCount := flag.Int("threads", 500, "initial thread count (between 1 and 2000. default is 500)")
	tagSvcThreads := flag.Int("tag-threads", 20, "threads to run for fetching image tags")
	outFileName := flag.String("output", "output.log", "output file to save findings to. default is output.log")
	maxCrawlDepth := flag.Int("max-crawl-depth", 8, "max charset depth to scan for (between 4 and 17). default is 8")
	outputOverwrite := flag.Bool("overwrite", false, "overwrite existing output file if it exists already")
	debug := flag.Bool("debug", false, "enable debug logs")
	trace := flag.Bool("trace", false, "enable trace level logs")
	outLogFile := flag.String("logs", "", "save output logs to given file")
	redisStore := flag.Bool("redis-out", false, "save the output to redis instance. uses REDIS_HOST & REDIS_PASSWORD environment variables")
	redisKeyPrefix := flag.Int("redis-key", -1, "redis key to use for storing data. key -1 will be compared against current records.")
	progressRefreshSeconds := flag.Int("refresh", 1, "refresh interval in seconds")

	flag.Parse()

	// Sanity Check
	if *outFileName == "" || *threadCount > 2000 || *threadCount < 0 || *maxCrawlDepth < 4 || *maxCrawlDepth > 17 {
		log.Fatalf("failed to parse input arguments")
	}

	// Debug Configuration
	if *debug {
		log.SetLevel(log.DebugLevel)
	}
	if *trace {
		log.SetLevel(log.TraceLevel)
	}
	if *outLogFile != "" {
		log.SetFormatter(&log.JSONFormatter{
			FieldMap: log.FieldMap{
				log.FieldKeyTime: "@timestamp",
				log.FieldKeyMsg:  "message",
			},
		})
		log.Infof("writing output logs to: %v ", *outLogFile)
		logFile, err := os.OpenFile(*outLogFile, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0644)
		if err != nil {
			log.Fatalf("error opening specified log file name for output logging. error = %v", err)
		}
		defer logFile.Close()
		mw := io.MultiWriter(os.Stdout, logFile)
		log.SetOutput(mw)
	}

	MAX_CRAWL_DEPTH = *maxCrawlDepth
	imageMap = make(map[string]map[string]bool)

	repositoryChan := make(chan *EcrRepositoryInfo, 1000)
	repositoryTagsChan := make(chan *EcrResult, 2000)

	saveResultsWg := &sync.WaitGroup{}
	saveResultsWg.Add(1)
	if *redisStore {
		if *redisKeyPrefix == -1 {
			log.Fatal("redis-key must be supplied and must be an integer")
		}
		go sendResultsToRedis(context.Background(), repositoryTagsChan, *redisKeyPrefix, saveResultsWg, *outputOverwrite)
	} else {
		// Save Results to disk

		// Output Overwrite configuration
		if _, err := os.Stat(*outFileName); errors.Is(err, os.ErrNotExist) {
			log.Debugf("output file does not exist and will be created")
		} else {
			if *outputOverwrite {
				log.Infof("overwriting existing output file")
			} else {
				log.Fatalf("output file exists")
			}
		}
		outFile, err := os.OpenFile(*outFileName, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
		if err != nil {
			log.Fatalf("could not open output file. error = %v", err)
		}
		defer outFile.Close()
		go saveOutputToDisk(outFile, repositoryTagsChan, saveResultsWg)
	}

	// Getting The Image Tags
	imageTagsWg := &sync.WaitGroup{}
	imageTagsWg.Add(*tagSvcThreads)
	for i := 0; i < *tagSvcThreads; i++ {
		go getImageTags(repositoryChan, repositoryTagsChan, imageTagsWg)
	}

	// Get Image Config
	// imageConfigWg := &sync.WaitGroup{}
	// imageConfigWg.Add(*configSvcThreads)
	// for i := 0; i < *configSvcThreads; i++ {
	// 	go getImageManifestConfig(repositoryTagsChan, repositoryConfigChan, imageConfigWg)
	// 	time.Sleep(time.Second)
	// }

	log.WithFields(log.Fields{"thread-count": *threadCount, "max-depth": MAX_CRAWL_DEPTH}).Info("starting public ecr gallery scan")

	// Display Progress line
	go displayProgress(*progressRefreshSeconds)

	if err := getAllEcrImages(*threadCount, repositoryChan); err != nil {
		log.Errorf("error fetching images from aws ecr. error = %v", err)
	}
	imageTagsWg.Wait()
	close(repositoryTagsChan)
	log.Info("done fetching all image tags. waiting to export results...")
	saveResultsWg.Wait()
	log.Info("exported results ...")
	statsLock.RLock()
	defer statsLock.RUnlock()
	log.Infof("found %v ecr repositories. exiting ...", foundImageCount)
}

func displayProgress(progressRefreshSeconds int) {
	time.Sleep(time.Second)
	bufferString := strings.Repeat(" ", 25)
	for {
		statsLock.RLock()
		fmt.Printf("Progress - [Found %v Repositories | %v Okay | %v Tags] - Active Goroutine Count: %v %s\r", foundImageCount, imageWithTagCount, totalTagsFoundCount, activeGoroutines, bufferString)
		statsLock.RUnlock()
		time.Sleep(time.Second * time.Duration(int64(progressRefreshSeconds)))
	}
}

func saveOutputToDisk(outFile io.Writer, resultChan chan *EcrResult, resultsWg *sync.WaitGroup) {
	defer resultsWg.Done()
	enc := json.NewEncoder(outFile)
	for result := range resultChan {
		err := enc.Encode(result)
		if err != nil {
			log.Errorf("error marshalling results. error = %v", err)
		}
	}
	log.Infof("done saving all results")
}
