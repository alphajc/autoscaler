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
	"sync"
	"time"

	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	"k8s.io/klog/v2"
)

// CredType represents credential type that includes:
//   GetServiceToken
//   AssumeTkeCredential
//   GetAgentCredential
type CredType int

const tokenBufferDuration = 10 // Token 有效期只剩 10s 重刷

// Credential type
const (
	ServiceTokenCredential CredType = iota // 对应 norm 的 get_service_token
	ServiceRoleCredential                  // 对应 norm 的 assume_tke_credential
	InnerUserCredential                    // 对应 norm 的 get_agent_credential
)

var (
	mutex   sync.Mutex
	service string
)

// Credential implements common.CredentialIface for norm,
// that is used by tencentcloud-sdk.
type Credential struct {
	duration    int
	expiredTime int64
	credType    CredType

	SecretId  string
	SecretKey string
	Token     string

	common.CredentialIface
}

// GetSecretKey returns a valid SecretKey at least 10s
func (c *Credential) GetSecretKey() string {
	mutex.Lock()
	if c.NeedRefresh() {
		c.Refresh()
	}
	mutex.Unlock()
	return c.SecretKey
}

// GetSecretId returns a valid SecretId at least 10s
func (c *Credential) GetSecretId() string {
	mutex.Lock()
	if c.NeedRefresh() {
		c.Refresh()
	}
	mutex.Unlock()
	return c.SecretId
}

// GetToken returns a valid Token at least 10s
func (c *Credential) GetToken() string {
	mutex.Lock()
	if c.NeedRefresh() {
		c.Refresh()
	}
	mutex.Unlock()
	return c.Token
}

// NewCredential return Credential
func NewCredential(credType CredType, duration int) *Credential {
	cred := &Credential{
		credType: credType,
		duration: duration,
	}
	return cred
}

// SetService set current service name for GetServiceToken
func SetService(svc string) {
	service = svc
}

// NeedRefresh checks token whether need to refresh
func (c *Credential) NeedRefresh() bool {
	if c.Token == "" || time.Now().Unix() > c.expiredTime-tokenBufferDuration {
		return true
	}
	return false
}

// Refresh refresh a new set of SecretKey/SecretID/Token
func (c *Credential) Refresh() {
	req := credentialReq{
		Duration: c.duration,
	}
	var resp *credentialResp
	var err error
	switch c.credType {
	case ServiceTokenCredential:
		if service == "" {
			klog.Errorf("get service token need to set service")
			return
		}
		req.Service = service
		resp, err = GetServiceToken(req)
	case ServiceRoleCredential:
		resp, err = AssumeTkeCredential(req)
	case InnerUserCredential:
		resp, err = GetAgentCredential(req)
	}
	if err != nil {
		klog.Error(err)
		return
	}

	c.SecretId = resp.Credentials.TmpSecretId
	c.SecretKey = resp.Credentials.TmpSecretKey
	c.Token = resp.Credentials.Token
	c.expiredTime = resp.ExpiredTime
}
