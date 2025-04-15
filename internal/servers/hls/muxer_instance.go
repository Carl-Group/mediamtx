package hls

import (
	"fmt"
	"io/ioutil"
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

	mi.stream.StartReader(mi)

	// Inject subtitle reference after short delay (to ensure muxer has started)
	go func() {
		time.Sleep(2 * time.Second)
		mi.injectSubtitleReference(muxerDirectory)
	}()

	return nil
}

// injectSubtitleReference appends EXT-X-MEDIA and updated EXT-X-STREAM-INF for subtitles
func (mi *muxerInstance) injectSubtitleReference(dir string) {
	mainPlaylist := filepath.Join(dir, "main_stream.m3u8")
	info, err := os.Stat(mainPlaylist)
	if err != nil || info.IsDir() {
		return
	}

	b, err := ioutil.ReadFile(mainPlaylist)
	if err != nil {
		mi.Log(logger.Warn, "unable to read playlist: %v", err)
		return
	}

	content := string(b)

	// Check if EXT-X-MEDIA is already present
	if strings.Contains(content, "EXT-X-MEDIA:TYPE=SUBTITLES") {
		return
	}

	subsLine := `#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID="subs",NAME="Deutsch",LANGUAGE="de",AUTOSELECT=YES,DEFAULT=YES,URI="subtitles.m3u8"`
	infLine := `#EXT-X-STREAM-INF:PROGRAM-ID=1,BANDWIDTH=800000,SUBTITLES="subs"`
	
	// prepend subtitle metadata
	newContent := "#EXTM3U\n#EXT-X-VERSION:3\n" + subsLine + "\n" + infLine + "\n" + filepath.Base(mainPlaylist) + "\n"
	
	err = ioutil.WriteFile(mainPlaylist, []byte(newContent), 0644)
	if err != nil {
		mi.Log(logger.Warn, "unable to write updated playlist: %v", err)
	}
}

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

	mi.hmuxer.Handle(w, ctx.Request)
}
