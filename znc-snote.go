package main

import (
	"bufio"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"text/template"

	flag "github.com/ogier/pflag"
)

var snoteRe = regexp.MustCompile(`\[(?P<time>(?:\d{2}:?){3})\]\s-(?P<server_name>\S+)-\s\*{3}\s(?P<snote>(?:REMOTE)?\S+):\s(?P<text>.*)`)
var Errorlogger = log.New(os.Stderr, "", 0)

var snoteType string
var ignoreRemote bool
var stripLeaders bool
var includeFileName bool
var fileOut string
var fileOutHandle *os.File
var fastMode bool
var formatString string
var nullDelimited bool

var printer *template.Template
var outChan = make(chan map[string]string)

const indent = "            "

func init() {
	flag.StringVarP(&snoteType, "snote", "s", "*", "sets the snote to look for, * matches all")
	flag.BoolVarP(&ignoreRemote, "ignore-remote", "a", false, "Sets whether or not REMOTE snotes are ignored")
	flag.BoolVar(&stripLeaders, "strip", false, "sets whether or not to strip the leading data from the snote")
	flag.StringVarP(&fileOut, "output", "o", "-", "sets the file to output the data to, - outputs to stdout")
	flag.BoolVar(&fastMode, "fast", false, "enable fast mode: files are scanned concurrently (does not guarantee order of results)")
	flag.StringVar(&formatString, "format", "{{.line}}", "Sets the format using a go template."+
		"        The following fields are available on the object:\n"+
		indent+"time           The time the snotice was recieved\n"+
		indent+"server_name    The name of the server that sent the snotice\n"+
		indent+"snote          The snote that occurred\n"+
		indent+"text           The content of the snote\n"+
		indent+"path           The full path to the file the snote was found in\n"+
		indent+"filename       The name of the file that the snote was found in\n"+
		indent+"dir            The name of the directory that the snote was found in\n  ")
	flag.BoolVarP(&nullDelimited, "null-delimited", "0", false, "Changes from newline delimited results to null delimited results")
}

type limitWaitGroup struct {
	limitChan chan struct{}
	wg        sync.WaitGroup
}

func (l *limitWaitGroup) Inc() {
	l.limitChan <- struct{}{}
	l.wg.Add(1)
}

func (l *limitWaitGroup) Done() {
	<-l.limitChan
	l.wg.Done()
}

func (l *limitWaitGroup) Wait() {
	l.wg.Wait()
}

func newLimitWaitGroup(limit int) *limitWaitGroup {
	return &limitWaitGroup{make(chan struct{}, limit), sync.WaitGroup{}}
}

var wg *limitWaitGroup
// TODO(A_D): json and xml output?
func main() {
	flag.Parse()
	if fastMode {
		wg = newLimitWaitGroup(runtime.NumCPU() * 10)
	} else {
		wg = newLimitWaitGroup(1)
	}

	if fileOut == "-" {
		fileOutHandle = os.Stdout
	} else {
		f, err := os.Create(fileOut)
		if err != nil {
			Errorlogger.Fatalf("could not open output file: %s", err)
		}
		fileOutHandle = f
		defer fileOutHandle.Close()
	}

	printer = template.Must(template.New("printer").Parse(formatString))
	go writeOut()
	fileList := flag.Args()
	if len(fileList) == 0 {
		fileList = append(fileList, "./")
	}
	for _, dir := range fileList {
		filepath.Walk(dir, scan)
	}
	wg.Wait()
	close(outChan)
}

func writeOut() {
	for v := range outChan {
		if err := printer.Execute(fileOutHandle, v); err != nil {
			os.Stdout.WriteString(err.Error())
		} else {
			if nullDelimited {
				fileOutHandle.Write([]byte{0x00})
			} else {
				fileOutHandle.Write([]byte{0x0A})
			}
		}
	}
}

func reMatchToMap(re *regexp.Regexp, text string) map[string]string {
	match := re.FindStringSubmatch(text)
	res := make(map[string]string)
	for i, name := range re.SubexpNames() {
		if i != 0 && name != "" {
			res[name] = match[i]
		}
	}
	return res
}

func scan(path string, info os.FileInfo, err error) error {
	if err != nil {
		Errorlogger.Printf("could not read %s: %s", path, err)
		return nil
	}
	if info.IsDir() {
		// We dont look at directories
		return nil
	}
	wg.Inc()
	go func() {
		defer wg.Done()
		f, err := os.Open(path)
		if err != nil {
			Errorlogger.Printf("could not open file %s: %s", path, err)
			//return nil
		}
		defer f.Close()

		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			text := scanner.Text()
			if !snoteRe.MatchString(text) {
				continue
			}
			res := reMatchToMap(snoteRe, text)
			if snoteType == "*" || strings.EqualFold(res["snote"], snoteType) || (!ignoreRemote && strings.EqualFold(res["snote"], "REMOTE"+snoteType)) {
				toPrint := text
				if stripLeaders {
					toPrint = res["text"]
				}
				if includeFileName {
					toPrint = path + ":" + toPrint
				}
				res["path"] = path
				res["dir"], res["filename"] = filepath.Split(path)
				res["line"] = text
				outChan <- res
			}

		}
	}()
	return nil
}
