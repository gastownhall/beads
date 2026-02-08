package icu

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNextPow2(t *testing.T) {
	assert.Equal(t, 32, NextPow2(32))
	assert.Equal(t, 64, NextPow2(33))
}

func TestUCharStr(t *testing.T) {
	var s UCharStr
	defer s.Free()
	assert.Equal(t, 0, s.len)
	assert.Equal(t, 0, s.cap)
	assert.Equal(t, "", s.GetString())
	s.SetString("hello, world!")
	assert.Equal(t, 13, s.len)
	assert.Equal(t, 64, s.cap)
	assert.Equal(t, "hello, world!", s.GetString())
	s.SetString("hello, world! this is a longer string and it requires a reallocation. we just keep adding words until it is bigger than 64 characters.")
	assert.Equal(t, 134, s.len)
	assert.Equal(t, 256, s.cap)
	assert.Equal(t, "hello, world! this is a longer string and it requires a reallocation. we just keep adding words until it is bigger than 64 characters.", s.GetString())
	s.SetString("hello, world!")
	assert.Equal(t, 13, s.len)
	assert.Equal(t, 256, s.cap)
	assert.Equal(t, "hello, world!", s.GetString())

	assert.Equal(t, "hello", s.GetSubstring(0,5))
	assert.Equal(t, "ello", s.GetSubstring(1,5))
	assert.Equal(t, "world", s.GetSubstring(7, 12))
}
