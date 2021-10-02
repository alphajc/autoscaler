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
	"bytes"
	"fmt"
	"io/ioutil"
	"net/http"
	"time"
)

type packer interface {
	packRequest(interfaceName string, reqObj interface{}) ([]byte, error)
	unPackResponse(data []byte, responseObj interface{}) error
	getResult() (int, string)
}

type httpClient struct {
	url     string
	timeout uint
	caller  string
	callee  string
	packer
}

func (c *httpClient) doRequest(interfaceName string, request interface{}, response interface{}) error {

	requestData, err := c.packRequest(interfaceName, request)
	if err != nil {
		return newNormError(ClientCommonError, "failed to pack request", err)
	}
	httpRequest, err := http.NewRequest("POST", c.url, bytes.NewReader(requestData))
	if err != nil {
		return newNormError(ClientCommonError, "failed to new request", err)
	}

	http.DefaultClient.Timeout = time.Duration(time.Duration(c.timeout) * time.Second)
	httpResponse, err := http.DefaultClient.Do(httpRequest)
	defer func() {
		if httpResponse != nil {
			httpResponse.Body.Close()
		}
	}()
	if err != nil {
		return newNormError(ClientHttpError, "failed to request norm", err)
	}

	if httpResponse.StatusCode != 200 {
		return newNormError(ClientHttpError, "status code is not 200", fmt.Errorf("StatusCode: %d", httpResponse.StatusCode))
	}
	body, err := ioutil.ReadAll(httpResponse.Body)
	if err != nil {
		return newNormError(ClientHttpError, "faild to read response body", err)
	}

	err = c.unPackResponse(body, response)
	if err != nil {
		return newNormError(ClientUnpackError, "failed to unpack response", err)
	}
	code, msg := c.getResult()
	if code != 0 {
		return newNormError(ClientHttpError, "server returned code is not zero", fmt.Errorf("%d: %s", code, msg))
	}
	return nil
}
