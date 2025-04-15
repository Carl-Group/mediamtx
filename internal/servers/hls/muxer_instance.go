package hls

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bluenviron/gohlslib/v2"
	"github.com/bluenviron/mediamtx/internal/conf"
	"github.com/bluenviron/mediamtx/internal/defs"
	"github.com/bluenviron/mediamtx/internal/logger"
	"github.com/bluenviron/mediamtx/internal/protocols/hls"
	"github.com/bluenviron/mediamtx/internal/stream"
	"github.com/gin-gonic/gin"
)

type muxerInstance struct {
	variant         conf.HLSVariant
	segmentCount    int
	segmentDuration conf.Duration
	partDuration    conf.Duration
	segmentMaxSize  conf.StringSize
	directory       string
	pathName        string
	stream          *stream.Stream
	bytesSent       *uint64
	parent          logger.Writer

	hmuxer *gohlslib.Muxer
}

func (mi *muxerInstance) initialize() error {
	var muxerDirectory string
	if mi.directory != "" {
		muxerDirectory = filepath.Join(mi.directory, mi.pathName)
		os.MkdirAll(muxerDirectory, 0o755)
	}

	mi.hmuxer = &gohlslib.Muxer{
		Variant:            gohlslib.MuxerVariant(mi.variant),
		SegmentCount:       mi.segmentCount,
		SegmentMinDuration: time.Duration(mi.segmentDuration),
		PartMinDuration:    time.Duration(mi.partDuration),
		SegmentMaxSize:     uint64(mi.segmentMaxSize),
		Directory:          muxerDirectory,
		OnEncodeError: func(err error) {
			mi.Log(logger.Warn, err.Error())
		},
	}

	err := hls.FromStream(mi.stream, mi, mi.hmuxer)
	if err != nil {
		return err
	}

	err = mi.hmuxer.Start()
	if err != nil {
		mi.stream.RemoveReader(mi)
		return err
	}

	mi.Log(logger.Info, "is converting into HLS, %s",
		defs.FormatsInfo(mi.stream.ReaderFormats(mi)))

	mi.Log(logger.Info, "👀 muxer_instance.go: Untertitel-Erweiterung aktiv!")

	mi.stream.StartReader(mi)

	return nil
}

// Log implements logger.Writer.
func (mi *muxerInstance) Log(level logger.Level, format string, args ...interface{}) {
	mi.parent.Log(level, format, args...)
}

func (mi *muxerInstance) close() {
	mi.stream.RemoveReader(mi)
	mi.hmuxer.Close()
	if mi.hmuxer.Directory != "" {
		os.Remove(mi.hmuxer.Directory)
	}
}

func (mi *muxerInstance) errorChan() chan error {
	return mi.stream.ReaderError(mi)
}

func (mi *muxerInstance) handleRequest(ctx *gin.Context) {
	w := &responseWriterWithCounter{
		ResponseWriter: ctx.Writer,
		bytesSent:      mi.bytesSent,
	}

	// Intercept playlist and inject subtitles if needed
	if strings.HasSuffix(ctx.Request.URL.Path, "/index.m3u8") {
		rw := &interceptWriter{ResponseWriter: w.ResponseWriter, buf: &bytes.Buffer{}}
		mi.hmuxer.Handle(rw, ctx.Request)
		content := rw.buf.String()

		// Only inject once, if not already injected
		if !strings.Contains(content, "#EXT-X-MEDIA:TYPE=SUBTITLES") {
			injection := "#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID=\"subs\",NAME=\"Deutsch\",DEFAULT=YES,AUTOSELECT=YES,FORCED=NO,LANGUAGE=\"de\",URI=\"subtitles.m3u8\"\n"
			content = strings.Replace(content, "#EXT-X-INDEPENDENT-SEGMENTS\n", "#EXT-X-INDEPENDENT-SEGMENTS\n"+injection, 1)
			mi.Log(logger.Info, "📺 Subtitle injection into index.m3u8 complete")
		}

		rw.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = io.Copy(w, strings.NewReader(content))
	} else {
		mi.hmuxer.Handle(w, ctx.Request)
	}
}

type interceptWriter struct {
	http.ResponseWriter
	buf *bytes.Buffer
}

func (w *interceptWriter) Write(p []byte) (int, error) {
	return w.buf.Write(p)
}
