// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"encoding/json"
)

func stockRawIntValue(values map[string]json.RawMessage, key string) int {
	value := stockRawIntPtr(values, key)
	if value == nil {
		return 0
	}
	return *value
}

func stockRawInt64Value(values map[string]json.RawMessage, key string) int64 {
	value := stockRawInt64Ptr(values, key)
	if value == nil {
		return 0
	}
	return *value
}
