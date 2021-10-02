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
	"fmt"
)

// Errno represent error code information
type Errno struct {
	Code int
	Msg  string
}

// GenMsg generates and returns error message
func (e Errno) GenMsg(msg string) string {
	return e.Msg + "[" + msg + "]"
}

// http client errors
var (
	ClientCommonError  = Errno{Code: -30000, Msg: "ClientCommonError"}
	ClientHttpError    = Errno{Code: -30001, Msg: "ClientHttpError"}
	ClientTimeoutError = Errno{Code: -30002, Msg: "ClientTimeoutError"}
	ClientUnpackError  = Errno{Code: -30002, Msg: "ClientUnpackError"}
)

// NormError represent norm-sdk error
type NormError struct {
	Code int
	Msg  string
	Err  error
}

// Error returns error string
func (e NormError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("DashboardError,Code : %d , Msg : %s, err : %s", e.Code, e.Msg, e.Err.Error())
	}
	return fmt.Sprintf("DashboardError,Code : %d , Msg : %s, err : nil", e.Code, e.Msg)
}

func newNormError(errno Errno, extraMsg string, err error) *NormError {
	return &NormError{Code: errno.Code, Msg: errno.GenMsg(extraMsg), Err: err}
}
