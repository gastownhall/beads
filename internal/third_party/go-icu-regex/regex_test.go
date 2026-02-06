// Copyright 2023 Dolthub, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package regex

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRegexpMatchLoop(t *testing.T) {
	// Just test concurrency.
	var wg sync.WaitGroup
	wg.Add(16)
	for i := 0; i < 16; i++ {
		go func() {
			defer wg.Done()
			for i := 0; i < 2048; i++ {
				TestRegexMatch(t)
			}
		}()
	}
	wg.Wait()
}

func TestRegexMatch(t *testing.T) {
	ctx := context.Background()
	regex := CreateRegex(512 * 1024)
	require.NoError(t, regex.SetRegexString(ctx, `abc+.*this st`, RegexFlags_None))
	err := regex.SetMatchString(ctx, "Find the abc in this string")
	require.NoError(t, err)
	ok, err := regex.Matches(ctx, 0, 0)
	require.NoError(t, err)
	require.True(t, ok)
	err = regex.SetMatchString(ctx, "Find the abc in this here string")
	require.NoError(t, err)
	ok, err = regex.Matches(ctx, 0, 0)
	require.NoError(t, err)
	require.False(t, ok)
	require.NoError(t, regex.Close())

	regex = CreateRegex(11)
	require.NoError(t, regex.SetRegexString(ctx, `[a-zA-Z0-9]{5} \w{4} aab`, RegexFlags_None))
	err = regex.SetMatchString(ctx, "Words like aab don't exist")
	require.NoError(t, err)
	ok, err = regex.Matches(ctx, 0, 0)
	require.NoError(t, err)
	require.True(t, ok)

	require.NoError(t, regex.SetRegexString(ctx, `^[aA]bcd[eE]$`, RegexFlags_None))
	err = regex.SetMatchString(ctx, "abcde")
	require.NoError(t, err)
	ok, err = regex.Matches(ctx, 0, 0)
	require.NoError(t, err)
	require.True(t, ok)
	err = regex.SetMatchString(ctx, "Abcde")
	require.NoError(t, err)
	ok, err = regex.Matches(ctx, 0, 0)
	require.NoError(t, err)
	require.True(t, ok)
	err = regex.SetMatchString(ctx, "AbcdE")
	require.NoError(t, err)
	ok, err = regex.Matches(ctx, 0, 0)
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, regex.Close())

	regex = CreateRegex(128)
	require.NoError(t, regex.SetRegexString(ctx, `^abcde$`, RegexFlags_None))
	err = regex.SetMatchString(ctx, "abcde")
	require.NoError(t, err)
	ok, err = regex.Matches(ctx, 0, 0)
	require.NoError(t, err)
	require.True(t, ok)
	err = regex.SetMatchString(ctx, "aBCDe")
	require.NoError(t, err)
	ok, err = regex.Matches(ctx, 0, 0)
	require.NoError(t, err)
	require.False(t, ok)
	require.NoError(t, regex.Close())
}

func TestRegexReplace(t *testing.T) {
	ctx := context.Background()
	regex := CreateRegex(512 * 1024)
	require.NoError(t, regex.SetRegexString(ctx, `[a-z]+`, RegexFlags_None))
	err := regex.SetMatchString(ctx, "abc def ghi")
	require.NoError(t, err)
	replacedStr, err := regex.Replace(ctx, "X", 1, 2)
	require.NoError(t, err)
	require.Equal(t, "abc X ghi", replacedStr)
	replacedStr, err = regex.Replace(ctx, "X", 1, 3)
	require.NoError(t, err)
	require.Equal(t, "abc def X", replacedStr)
	replacedStr, err = regex.Replace(ctx, "X", 1, 0)
	require.NoError(t, err)
	require.Equal(t, "X X X", replacedStr)
	require.NoError(t, regex.Close())
}

func TestRegexIndexOf(t *testing.T) {
	ctx := context.Background()
	regex := CreateRegex(1024)
	require.NoError(t, regex.SetRegexString(ctx, `[a-j]+`, RegexFlags_None))
	err := regex.SetMatchString(ctx, "abc def ghi")
	require.NoError(t, err)
	idx, err := regex.IndexOf(ctx, 1, 1, false)
	require.NoError(t, err)
	require.Equal(t, 1, idx)
	idx, err = regex.IndexOf(ctx, 4, 1, false)
	require.NoError(t, err)
	require.Equal(t, 5, idx)
	idx, err = regex.IndexOf(ctx, 8, 1, false)
	require.NoError(t, err)
	require.Equal(t, 9, idx)
	idx, err = regex.IndexOf(ctx, 1, 2, false)
	require.NoError(t, err)
	require.Equal(t, 5, idx)
	idx, err = regex.IndexOf(ctx, 1, 3, false)
	require.NoError(t, err)
	require.Equal(t, 9, idx)
	idx, err = regex.IndexOf(ctx, 1, 4, false)
	require.NoError(t, err)
	require.Equal(t, 0, idx)
	idx, err = regex.IndexOf(ctx, 1, 1, true)
	require.NoError(t, err)
	require.Equal(t, 4, idx)
	idx, err = regex.IndexOf(ctx, 4, 1, true)
	require.NoError(t, err)
	require.Equal(t, 8, idx)
	idx, err = regex.IndexOf(ctx, 8, 1, true)
	require.NoError(t, err)
	require.Equal(t, 12, idx)
	idx, err = regex.IndexOf(ctx, 1, 2, true)
	require.NoError(t, err)
	require.Equal(t, 8, idx)
	idx, err = regex.IndexOf(ctx, 1, 3, true)
	require.NoError(t, err)
	require.Equal(t, 12, idx)
	idx, err = regex.IndexOf(ctx, 1, 4, true)
	require.NoError(t, err)
	require.Equal(t, 0, idx)
	require.NoError(t, regex.SetMatchString(ctx, "klmno fghij abcde"))
	idx, err = regex.IndexOf(ctx, 1, 1, false)
	require.NoError(t, err)
	require.Equal(t, 7, idx)
	idx, err = regex.IndexOf(ctx, 1, 1, true)
	require.NoError(t, err)
	require.Equal(t, 12, idx)
	require.NoError(t, regex.Close())
}

func TestRegexSubstring(t *testing.T) {
	ctx := context.Background()
	regex := CreateRegex(1024)
	require.NoError(t, regex.SetRegexString(ctx, `[a-z]+`, RegexFlags_None))
	err := regex.SetMatchString(ctx, "abc def ghi")
	require.NoError(t, err)
	substr, ok, err := regex.Substring(ctx, 1, 1)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "abc", substr)
	substr, ok, err = regex.Substring(ctx, 4, 1)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "def", substr)
	substr, ok, err = regex.Substring(ctx, 8, 1)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "ghi", substr)
	substr, ok, err = regex.Substring(ctx, 1, 2)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "def", substr)
	substr, ok, err = regex.Substring(ctx, 1, 3)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "ghi", substr)
	substr, ok, err = regex.Substring(ctx, 1, 4)
	require.NoError(t, err)
	require.False(t, ok)
	require.Equal(t, "", substr)
	require.NoError(t, regex.SetMatchString(ctx, "ghx dey abz"))
	substr, ok, err = regex.Substring(ctx, 1, 1)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "ghx", substr)
	require.NoError(t, regex.Close())
}
