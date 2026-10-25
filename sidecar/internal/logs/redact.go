package logs

import (
	"bufio"
	"errors"
	"io"
	"regexp"
	"strings"
)

// At logLevel debug, MediaMTX (from v1.21.3) logs every outbound HTTP request with its body, and so the JSON it posts
// to the sidecar's /internal/auth for each connection: password, token and query in clear (stream keys, guest keys,
// viewer tickets, RTMP credentials). Only headers are redacted there. The viewer, its live tail and the download blank
// those three fields, in both of MediaMTX's formats: plain, where the body is a line of its own, and structured, where
// it sits inside a quoted message (\"password\":\"...\"). A value cut off by the end of the line is blanked to there.
// The file on disk keeps them, which is why the docs warn before debug.
var (
	reAuthField       = regexp.MustCompile(`"(password|token|query)":"(?:\\.|[^"\\])*(?:"|\\*$)`)
	reQuotedAuthField = regexp.MustCompile(`\\"(password|token|query)\\":\\"(?:\\\\(?:\\\\|\\"|\\[^\\"]|[^\\])|\\[^\\"]|[^\\])*(?:\\"|\\*$)`)
)

const redacted = "<redacted>"

// Redact blanks the secrets of MediaMTX's auth requests in one log line (see above); other text is left as it is.
func Redact(s string) string {
	s = reAuthField.ReplaceAllString(s, `"$1":"`+redacted+`"`)
	return reQuotedAuthField.ReplaceAllString(s, `\"$1\":\"`+redacted+`\"`)
}

// maxRedactLine bounds the memory one line may take while a download is redacted. MediaMTX's longest lines are
// request bodies, which it cuts at 10 KiB; a longer line is redacted in pieces of this size.
const maxRedactLine = 1 << 20

// copyRedacted copies r to w line by line, each line through Redact.
func copyRedacted(w io.Writer, r io.Reader) error {
	br := bufio.NewReaderSize(r, 64<<10)
	var line []byte
	for {
		chunk, err := br.ReadSlice('\n')
		line = append(line, chunk...)
		if errors.Is(err, bufio.ErrBufferFull) && len(line) < maxRedactLine {
			continue
		}
		if len(line) > 0 {
			text := string(line)
			end := len(strings.TrimRight(text, "\r\n"))
			if _, werr := io.WriteString(w, Redact(text[:end])+text[end:]); werr != nil {
				return werr
			}
			line = line[:0]
		}
		switch {
		case err == nil, errors.Is(err, bufio.ErrBufferFull):
		case errors.Is(err, io.EOF):
			return nil
		default:
			return err
		}
	}
}
