package main

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/valyala/fasthttp"
)

const (
	DOCKER_USER_AGENT                = "docker/20.10.23+dfsg1 go/go1.19.5 git-commit/6051f14 kernel/6.0.0-kali6-amd64 os/linux arch/amd64 UpstreamClient(Docker-Client/20.10.23+dfsg1 (linux))"
	REGISTRY_GET_MANIFEST_URI_FORMAT = "https://public.ecr.aws/v2/%v/%v/manifests/%v"
	REGISTRY_GET_CONFIG_URI_FORMAT   = "https://public.ecr.aws/v2/%v/%v/blobs/%v"
	REGISTRY_AUTH_TOKEN_URL          = "https://public.ecr.aws/token/?scope=*:pull&service=public.ecr.aws"

	//REGISTRY_CONFIG_CONTENT_TYPE = "application/vnd.docker.container.image.v1+json"
)

var (
	REGISTRY_MANIFEST_CONTENT_TYPE_HEADERS = []string{
		"application/vnd.docker.distribution.manifest.v2+json",
		"application/vnd.docker.distribution.manifest.list.v2+json",
		"application/vnd.oci.image.index.v1+json",
		"application/vnd.docker.distribution.manifest.v1+prettyjws",
		"application/vnd.oci.image.manifest.v1+json",
		"application/json"}

	registryRequestPool = sync.Pool{
		New: func() interface{} {
			req := &fasthttp.Request{}
			req.Header.Set("User-Agent", DOCKER_USER_AGENT)
			req.Header.SetMethod("GET")
			return req
		},
	}
)

type RegistryManager struct {
	TokenLock *sync.RWMutex
	AuthToken string
}

func GetNewRegistryManager() *RegistryManager {
	rm := &RegistryManager{
		TokenLock: &sync.RWMutex{},
	}
	rm.RefreshAuthToken()
	return rm
}

func (rm *RegistryManager) RefreshAuthToken() error {
	time.Sleep(time.Second * 10)
	log.WithFields(log.Fields{"state": "registry", "action": "token-refresh"}).Debugf("attempting token refresh")
	rm.TokenLock.Lock()
	defer rm.TokenLock.Unlock()
	client := httpClientPool.Get().(*fasthttp.Client)
	defer httpClientPool.Put(client)
	req := registryRequestPool.Get().(*fasthttp.Request)
	defer registryRequestPool.Put(req)
	req.SetRequestURI(REGISTRY_AUTH_TOKEN_URL)
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseResponse(resp)
	if err := client.Do(req, resp); err != nil {
		log.WithFields(log.Fields{"state": "registry", "action": "token-refresh"}).Debugf("error getting auth token from ecr")
		return err
	}
	if resp.StatusCode() != 200 {
		log.WithFields(log.Fields{"state": "registry", "action": "token-refresh", "status-code": resp.StatusCode()}).Debugf(string(resp.Body()))
		return fmt.Errorf("error fetching auth token")
	}
	respStruct := &AuthTokenResponse{}
	if err := json.Unmarshal(resp.Body(), respStruct); err != nil {
		log.WithFields(log.Fields{"state": "registry", "action": "token-refresh", "status-code": resp.StatusCode(), "errmsg": err.Error()}).Debugf(string(resp.Body()))
		log.Error("error decoding server response")
		return err
	}
	rm.AuthToken = respStruct.Token
	log.WithFields(log.Fields{"state": "registry", "action": "token-refresh"}).Debugf("token refresh done")
	return nil
}

func getRepositoryManifestUrl(repo *EcrRepositoryInfo, sha256checksum string) string {
	return fmt.Sprintf(REGISTRY_GET_MANIFEST_URI_FORMAT, repo.PrimaryRegistryAliasName, repo.RepositoryName, sha256checksum)
}

func getConfigRefererUrl(repo *EcrRepositoryInfo, sha256checksum string) string {
	return fmt.Sprintf(REGISTRY_GET_CONFIG_URI_FORMAT, repo.PrimaryRegistryAliasName, repo.RepositoryName, sha256checksum)
}

func (rm *RegistryManager) getConfigFromImageManifest(repo *EcrRepositoryInfo, sha256sum string, manifestResponse *ManifestResponse) (map[string]string, error) {

	response := make(map[string]string, 1)

	client := httpClientPool.Get().(*fasthttp.Client)
	defer httpClientPool.Put(client)
	req := registryRequestPool.Get().(*fasthttp.Request)
	defer registryRequestPool.Put(req)
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseResponse(resp)

	config := manifestResponse.Config
	attempt := 0

	req.SetRequestURI(getConfigRefererUrl(repo, config.Digest))
	for {
		if err := rm.RefreshAuthToken(); err != nil {
			log.Error("attepting to refresh auth token failed!")
		}
		req.Header.Del("Authorization")
		rm.TokenLock.RLock()
		req.Header.Add("Authorization", fmt.Sprintf("Bearer %v", rm.AuthToken))
		rm.TokenLock.RUnlock()
		if err := client.DoRedirects(req, resp, 1); err != nil {
			attempt += 1
			log.WithFields(log.Fields{"state": "registry", "action": "config-fetch"}).Debugf("error making http request")
			if err := rm.RefreshAuthToken(); err != nil {
				log.Error("attepting to refresh auth token failed!")
			}
			if attempt < 2 {
				continue
			}
			break
			//return nil, err
		}
		if resp.StatusCode() != 200 {
			log.WithFields(log.Fields{"state": "registry", "action": "config-fetch", "status-code": resp.StatusCode()}).Debugf(string(resp.Body()))
			attempt += 1
			if err := rm.RefreshAuthToken(); err != nil {
				log.Error("attepting to refresh auth token failed!")
			}
			if attempt < 2 {
				continue
			}
			break
			//return nil, fmt.Errorf("unknown http status code recieved! %v", resp.Body())
		}
		response[config.Digest] = string(resp.Body())
		break
	}

	return response, nil
}

func (rm *RegistryManager) getManifestForImageTag(repo *EcrRepositoryInfo, sha256checksum string) (*ManifestResponse, error) {
	attempt := 0
	client := httpClientPool.Get().(*fasthttp.Client)
	defer httpClientPool.Put(client)
	req := registryRequestPool.Get().(*fasthttp.Request)
	defer registryRequestPool.Put(req)
	req.SetRequestURI(getRepositoryManifestUrl(repo, sha256checksum))
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseResponse(resp)

	for {
		if err := rm.RefreshAuthToken(); err != nil {
			log.Error("attepting to refresh auth token failed!")
		}
		req.Header.Del("Authorization")
		rm.TokenLock.RLock()
		req.Header.Add("Authorization", fmt.Sprintf("Bearer %v", rm.AuthToken))
		rm.TokenLock.RUnlock()
		if err := client.Do(req, resp); err != nil {
			attempt += 1
			log.WithFields(log.Fields{"state": "registry", "action": "manifest-fetch"}).Debugf("error making http request")
			if attempt < 2 {
				continue
			}
			return nil, err
		}
		if resp.StatusCode() != 200 {
			log.WithFields(log.Fields{"state": "registry", "action": "manifest-fetch", "status-code": resp.StatusCode()}).Debugf(string(resp.Body()))
			attempt += 1
			if err := rm.RefreshAuthToken(); err != nil {
				log.Error("attepting to refresh auth token failed!")
			}
			if attempt < 2 {
				continue
			}
			return nil, fmt.Errorf("unknown http status code recieved! %v", string(resp.Body()))
		}
		respStruct := &ManifestResponse{}
		if err := json.Unmarshal(resp.Body(), respStruct); err != nil {
			log.WithFields(log.Fields{"state": "registry", "action": "manifest-fetch", "status-code": resp.StatusCode(), "errmsg": err.Error()}).Debugf(string(resp.Body()))
			return nil, err
		}
		return respStruct, nil
	}
}

type ManifestResponse struct {
	SchemaVersion int            `json:"schemaVersion"`
	MediaType     string         `json:"mediaType"`
	Config        LayerConfig    `json:"config"`
	Layers        []*LayerConfig `json:"layers"`
}

type LayerConfig struct {
	MediaType string `json:"mediaType"`
	Size      int    `json:"size"`
	Digest    string `json:"digest"`
}

type AuthTokenResponse struct {
	Token string `json:"token"`
}
