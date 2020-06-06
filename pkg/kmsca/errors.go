/*
Copyright 2020 Skyscanner Limited.

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

package kmsca

import (
	"errors"
)

var (
	// ErrCodeNotFoundException The request was rejected because the specified entity or resource could not be found.
	ErrCodeNotFoundException = errors.New("NotFoundException")

	// ErrUnknownKeyType The type of the of the public key is unknown.
	ErrUnknownKeyType = errors.New("UnknownKeyType")
)