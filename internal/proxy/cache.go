package proxy

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
)

type CacheTracker struct {
	sync.RWMutex
	PrefixHashes map[string]bool 
}

var CacheStore = &CacheTracker{
	PrefixHashes: make(map[string]bool),
}

func EvaluatePromptCache(messages []ChatMessage) bool {
	if len(messages) <= 1 {
		return false 
	}

	var prefixBuilder strings.Builder
	for i := 0; i < len(messages)-1; i++ {
		prefixBuilder.WriteString(messages[i].Role + ":" + messages[i].Content + "\n")
	}

	prefixStr := prefixBuilder.String()
	if len(prefixStr) == 0 {
		return false
	}

	hasher := sha256.New()
	hasher.Write([]byte(prefixStr))
	hashKey := hex.EncodeToString(hasher.Sum(nil))

	CacheStore.Lock()
	defer CacheStore.Unlock()

	hit := CacheStore.PrefixHashes[hashKey]
	CacheStore.PrefixHashes[hashKey] = true

	return hit
}
