package main

import (
	"encoding/json"
	"fmt"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/valyala/fasthttp"
)

type AwsEcrSearchResponse struct {
	RepositoryCatalogSearchResultList []*EcrRepositoryInfo `json:"repositoryCatalogSearchResultList"`
	TotalResults                      int                  `json:"totalResults"`
	NextToken                         string               `json:"nextToken"`
}

type EcrRepositoryInfo struct {
	RepositoryName           string   `json:"repositoryName"`
	PrimaryRegistryAliasName string   `json:"primaryRegistryAliasName"`
	DisplayName              string   `json:"displayName"`
	LogoURL                  *string  `json:"logoUrl,omitempty"`
	RepositoryDescription    *string  `json:"repositoryDescription,omitempty"`
	OperatingSystems         []string `json:"operatingSystems,omitempty"`
	Architectures            []string `json:"architectures,omitempty"`
	RegistryVerified         bool     `json:"registryVerified"`
	DownloadCount            int      `json:"downloadCount"`
}

type EcrResult struct {
	RepositoryInfo *EcrRepositoryInfo
	ImageTags      *DescribeImageTagResponse
	ManifestConfig map[string]string
}

type DescribeImageTagResponse struct {
	ImageTagDetails *[]ImageTagDetails `json:"imageTagDetails"`
}

type RedisMetaDataValue struct {
	TotalImages   int    `json:"totalImages"`
	TotalFindings int    `json:"totalFindings"`
	NewFindings   int    `json:"newFindings"`
	DateTime      string `json:"timestamp"`
	SetKey        string `json:"setKey"`
	DiffKey       string `json:"diffSetKey"`
}

type ImageTagDetails struct {
	ImageTag    string      `json:"imageTag"`
	CreatedAt   time.Time   `json:"createdAt"`
	ImageDetail ImageDetail `json:"imageDetail"`
}

type ImageDetail struct {
	ImageDigest            string    `json:"imageDigest"`
	ImageSizeInBytes       int       `json:"imageSizeInBytes"`
	ImagePushedAt          time.Time `json:"imagePushedAt"`
	ImageManifestMediaType string    `json:"imageManifestMediaType"`
	ArtifactMediaType      string    `json:"artifactMediaType"`
}

func makeEcrSearchRequest(searchTerm, nextToken string) (*AwsEcrSearchResponse, error) {
	requestStruct := &AwsEcrSearchRequest{
		SearchTerm: searchTerm,
		NextToken:  nextToken,
		SortConfiguration: &SortConfiguration{
			SortKey: "POPULARITY",
		},
	}
	client := httpClientPool.Get().(*fasthttp.Client)
	defer httpClientPool.Put(client)
	req := searchRequestPool.Get().(*fasthttp.Request)
	defer searchRequestPool.Put(req)
	jsonBytes, err := json.Marshal(requestStruct)
	if err != nil {
		log.Error("error constructing request body")
		return nil, err
	}
	req.SetRequestURI(AWS_ECR_ENDPOINT_URI + AWS_ECR_SEARCH_REQUEST_URI)
	req.SetBody(jsonBytes)
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseResponse(resp)
	if err := client.Do(req, resp); err != nil {
		log.Error("error making http request")
		return nil, err
	}
	if resp.StatusCode() != 200 {
		log.Errorf("error making http request [%d] - %s", resp.StatusCode(), resp.Body())
		return nil, fmt.Errorf("error making http request")
	}

	respStruct := &AwsEcrSearchResponse{}
	if err := json.Unmarshal(resp.Body(), respStruct); err != nil {
		log.Error("error decoding server response")
		return nil, err
	}
	return respStruct, nil
}

type AwsEcrSearchRequest struct {
	SearchTerm        string             `json:"searchTerm"`
	NextToken         string             `json:"nextToken,omitempty"`
	SortConfiguration *SortConfiguration `json:"sortConfiguration"`
}

type SortConfiguration struct {
	SortKey string `json:"sortKey"`
}

type DescribeImageTagRequest struct {
	RegistyrAliasName string `json:"registryAliasName"`
	RepositoryName    string `json:"repositoryName"`
}

func describeImageTags(repo *EcrRepositoryInfo) (*DescribeImageTagResponse, error) {
	requestStruct := &DescribeImageTagRequest{
		RegistyrAliasName: repo.PrimaryRegistryAliasName,
		RepositoryName:    repo.RepositoryName,
	}
	client := httpClientPool.Get().(*fasthttp.Client)
	defer httpClientPool.Put(client)
	req := searchRequestPool.Get().(*fasthttp.Request)
	defer searchRequestPool.Put(req)
	jsonBytes, err := json.Marshal(requestStruct)
	if err != nil {
		log.Error("error constructing request body")
		return nil, err
	}
	req.SetRequestURI(AWS_ECR_ENDPOINT_URI + AWS_ECR_DESCRIBE_IMAGE_TAGS_URI)
	req.SetBody(jsonBytes)
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseResponse(resp)
	if err := client.Do(req, resp); err != nil {
		log.Error("error making http request")
		return nil, err
	}
	if resp.StatusCode() == 400 {
		return nil, fmt.Errorf("RepositoryNotFoundException")
	}
	if resp.StatusCode() != 200 {
		log.Debugf("error making http request [%d] - %s", resp.StatusCode(), resp.Body())
		return nil, fmt.Errorf("error making http request")
	}

	respStruct := &DescribeImageTagResponse{}
	if err := json.Unmarshal(resp.Body(), respStruct); err != nil {
		log.Error("error decoding server response")
		return nil, err
	}
	return respStruct, nil
}
