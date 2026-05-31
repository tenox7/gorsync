package receiver

import (
	"fmt"
	"io"
)

// chunkSize is rsync's CHUNK_SIZE (rsync/rsync.h): the maximum length of a
// single literal (uncompressed) token. Real rsync rejects anything longer with
// "invalid uncompressed token length"; we mirror that to stay protocol
// conformant and to catch a peer that frames literal data incorrectly.
const chunkSize = 32 * 1024

// rsync/token.c:recvToken
func (rt *Transfer) recvToken() (token int32, data []byte, _ error) {
	var err error
	token, err = rt.Conn.ReadInt32()
	if err != nil {
		return 0, nil, err
	}
	if token <= 0 {
		return token, nil, nil
	}
	if token > chunkSize {
		return 0, nil, fmt.Errorf("invalid uncompressed token length %d", token)
	}
	data = make([]byte, int(token))
	if _, err := io.ReadFull(rt.Conn.Reader, data); err != nil {
		return 0, nil, err
	}
	return token, data, nil
}
