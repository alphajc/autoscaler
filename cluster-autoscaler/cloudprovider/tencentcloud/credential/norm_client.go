/*
Copyright 2016 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package credential

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"time"

	"github.com/bitly/go-simplejson"
	"k8s.io/klog/v2"
)

// Norm creadentials is http action
const (
	NormGetAgentCredential  = "NORM.GetAgentCredential"
	NormAssumeTkeCredential = "NORM.AssumeTkeCredential"
	NormGetServiceToken     = "NORM.GetServiceToken"
)

// Environment variables
const (
	EnvClusterID = "CLUSTER_ID"
	EnvAppID     = "APPID"
)

// NormRequest represents a request to send to norm server
type NormRequest map[string]interface{}

// NormResponse represents a response form norm server
type NormResponse struct {
	Code     int         `json:"returnValue"`
	Msg      string      `json:"returnMsg"`
	Version  string      `json:"version"`
	Password string      `json:"password"`
	Data     interface{} `json:"returnData"`
}

// NormClient represents a norm client
type NormClient struct {
	httpClient
	request  NormRequest
	response NormResponse
}

func getNormUrl() string {
	url := os.Getenv("QCLOUD_NORM_URL")
	if url == "" {
		url = "http://169.254.0.40:80/norm/api"
	}
	return url
}

// NewNormClient returns a NormClient
func NewNormClient() *NormClient {
	c := &NormClient{
		request:  NormRequest{},
		response: NormResponse{},
	}
	c.httpClient = httpClient{
		url:     getNormUrl(),
		timeout: 10,
		caller:  "cloudprovider",
		callee:  "NORM",
		packer:  c,
	}
	return c
}

func setNormReqExt(body []byte) []byte {
	js, err := simplejson.NewJson(body)
	if err != nil {
		klog.Error("SetNormReqExt NewJson error,", err)
		return nil
	}
	if os.Getenv(EnvClusterID) != "" {
		js.SetPath([]string{"interface", "para", "unClusterId"}, os.Getenv(EnvClusterID))
		//klog.V(4).Info("SetNormReqExt set unClusterId", os.Getenv(ENV_CLUSTER_ID))
	}
	if os.Getenv(EnvAppID) != "" {
		js.SetPath([]string{"interface", "para", "appId"}, os.Getenv(EnvAppID))
		//klog.V(4).Info("SetNormReqExt set appId", os.Getenv(ENV_APP_ID))
	}

	out, err := js.Encode()
	if err != nil {
		klog.Error("SetNormReqExt Encode error,", err)
		return body
	}

	return out
}

func (c *NormClient) packRequest(interfaceName string, reqObj interface{}) ([]byte, error) {
	c.request = map[string]interface{}{
		"eventId":   rand.Uint32(),
		"timestamp": time.Now().Unix(),
		"caller":    c.httpClient.caller,
		"callee":    c.httpClient.callee,
		"version":   "1",
		"password":  "cloudprovider",
		"interface": map[string]interface{}{
			"interfaceName": interfaceName,
			"para":          reqObj,
		},
	}

	b, err := json.Marshal(c.request)
	if err != nil {
		klog.Error("packRequest failed:", err, ", req:", c.request)
		return nil, err
	}
	b = setNormReqExt(b)
	return b, nil
}

func (c *NormClient) unPackResponse(data []byte, responseData interface{}) (err error) {
	c.response.Data = responseData
	err = json.Unmarshal(data, &c.response)
	if err != nil {
		klog.Error("Unmarshal goods response err:", err)
		return

	}
	return
}

func (c *NormClient) getResult() (int, string) {
	return c.response.Code, c.response.Msg
}

type credentialReq struct {
	Duration int    `json:"duration"`
	Service  string `json:"service,omitempty"`
}

type credentialResp struct {
	Credentials struct {
		Token        string `json:"token"`
		TmpSecretId  string `json:"tmpSecretId"`
		TmpSecretKey string `json:"tmpSecretKey"`
	} `json:"credentials"`
	ExpiredTime int64  `json:"expiredTime"`
	Expiration  string `json:"expiration,omitempty"`
}

// GetAgentCredential get credential by NORM.GetAgentCredential
func GetAgentCredential(req credentialReq) (*credentialResp, error) {
	c := NewNormClient()
	resp := &credentialResp{}
	err := c.doRequest(NormGetAgentCredential, req, resp)
	if err != nil {
		return resp, fmt.Errorf("failed to get credential by NormGetAgentCredential: %s", err)
	}
	return resp, nil
}

// AssumeTkeCredential get credential by NORM.AssumeTkeCredential
func AssumeTkeCredential(req credentialReq) (*credentialResp, error) {
	c := NewNormClient()
	resp := &credentialResp{}
	err := c.doRequest(NormAssumeTkeCredential, req, resp)
	if err != nil {
		return resp, fmt.Errorf("failed to get credential by NormAssumeTkeCredential: %s", err)
	}
	return resp, nil
}

// GetServiceToken get credential by NORM.GetServiceToken
func GetServiceToken(req credentialReq) (*credentialResp, error) {
	c := NewNormClient()
	resp := &credentialResp{}
	err := c.doRequest(NormGetServiceToken, req, resp)
	if err != nil {
		return resp, fmt.Errorf("failed to get credential by NormGetServiceToken: %s", err)
	}
	return resp, nil
}
