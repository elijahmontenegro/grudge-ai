// Package chunkkey is the shared (message_id, chunk_index) <-> ANN index key
// encoding. The oracle writes keys into the index; the search consumer reads
// them back off index results. Both go through here so they agree without the
// generic service/annindex package knowing anything about grudge's chunk model.
package chunkkey

import (
	"strconv"
	"strings"
)

// Make encodes a chunk identity as an ANN index key: the message id and chunk
// index NUL-joined. Message ids never contain NUL, so Split round-trips it.
func Make(messageID string, chunkIndex int) string {
	return messageID + "\x00" + strconv.Itoa(chunkIndex)
}

// Split decodes a key produced by Make back into (messageID, chunkIndex). A key
// without the separator (defensive) yields the whole string and index 0.
func Split(key string) (string, int) {
	mid, idx, found := strings.Cut(key, "\x00")
	if !found {
		return key, 0
	}
	n, _ := strconv.Atoi(idx)
	return mid, n
}
