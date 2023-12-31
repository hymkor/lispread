package main

import (
	"bufio"
	"context"
	"encoding/csv"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"

	"github.com/mattn/go-colorable"
	"github.com/mattn/go-runewidth"
	"github.com/mattn/go-tty"

	"github.com/nyaosorg/go-readline-ny"
	"github.com/nyaosorg/go-readline-ny/keys"
	"github.com/nyaosorg/go-readline-skk"
	"github.com/nyaosorg/go-windows-mbcs"

	"github.com/hymkor/gmnlisp"
)

type _CodeFlag int

const (
	nonBomUtf8 _CodeFlag = 0
	isAnsi     _CodeFlag = 1
	hasBom     _CodeFlag = 2
	hasCR      _CodeFlag = 4
)

const bomCode = "\uFEFF"

var version string

func cutStrInWidth(s string, cellwidth int) (string, int) {
	w := 0
	for n, c := range s {
		w1 := runewidth.RuneWidth(c)
		if w+w1 > cellwidth {
			return s[:n], w
		}
		w += w1
	}
	return s, w
}

const (
	CURSOR_COLOR     = "\x1B[0;40;37;1;7m"
	CELL1_COLOR      = "\x1B[0;48;5;235;37;1m"
	CELL2_COLOR      = "\x1B[0;40;37;1m"
	ERASE_LINE       = "\x1B[0m\x1B[0K"
	ERASE_SCRN_AFTER = "\x1B[0m\x1B[0J"
)

type LineView struct {
	CSV       []Cell
	CellWidth int
	MaxInLine int
	CursorPos int
	Reverse   bool
	ReferFunc func(context.Context, *gmnlisp.World, int, int) (string, error)
	StartX    int
	StartY    int
	Out       io.Writer
}

var replaceTable = strings.NewReplacer(
	"\r", "\u240A",
	"\x1B", "\u241B",
	"\n", "\u2936", // arrow pointing downwards then curving leftwards
	"\t", "\u21E5") // rightwards arrow to bar (rightward tab)

// See. en.wikipedia.org/wiki/Unicode_control_characters#Control_pictures

func (v LineView) Draw(ctx context.Context, row int) {
	leftWidth := v.MaxInLine
	i := 0
	csvs := v.CSV
	for len(csvs) > 0 {
		s := csvs[0].Eval(ctx, row, v.StartX+i, v.ReferFunc)
		csvs = csvs[1:]
		nextI := i + 1

		cw := v.CellWidth
		for len(csvs) > 0 && csvs[0].Empty() && nextI != v.CursorPos {
			cw += v.CellWidth
			csvs = csvs[1:]
			nextI++
		}
		if cw > leftWidth || len(csvs) <= 0 {
			cw = leftWidth
		}
		s = replaceTable.Replace(s)
		ss, w := cutStrInWidth(s, cw)
		if i == v.CursorPos {
			io.WriteString(v.Out, CURSOR_COLOR)
		} else if v.Reverse {
			io.WriteString(v.Out, CELL2_COLOR)
		} else {
			io.WriteString(v.Out, CELL1_COLOR)
		}
		io.WriteString(v.Out, ss)
		leftWidth -= w
		for j := cw - w; j > 0; j-- {
			v.Out.Write([]byte{' '})
			leftWidth--
		}
		if leftWidth <= 0 {
			break
		}
		i = nextI
	}
	io.WriteString(v.Out, ERASE_LINE)
}

var cache = map[int]string{}

const CELL_WIDTH = 14

func view(ctx context.Context, in *MemoryCsv, csrpos, csrlin, w, h int, referf func(context.Context, *gmnlisp.World, int, int) (string, error), out io.Writer) (int, error) {
	reverse := false
	count := 0
	lfCount := 0
	var cancel func()
	ctx, cancel = context.WithTimeout(ctx, time.Second)
	defer cancel()
	for {
		if count >= h {
			return lfCount, nil
		}
		record, err := in.Read()
		if err == io.EOF {
			return lfCount, nil
		}
		if err != nil {
			return lfCount, err
		}
		if count > 0 {
			lfCount++
			fmt.Fprintln(out, "\r") // "\r" is for Linux and go-tty
		}
		evaled_records := make([]Cell, 0, len(record))
		for _, v := range record {
			evaled_records = append(evaled_records, Cell{source: v})
		}
		var buffer strings.Builder
		v := LineView{
			CSV:       evaled_records,
			CellWidth: CELL_WIDTH,
			MaxInLine: w,
			Reverse:   reverse,
			ReferFunc: referf,
			Out:       &buffer,
			StartX:    in.StartX,
			StartY:    in.StartY,
		}
		if count == csrlin {
			v.CursorPos = csrpos
		} else {
			v.CursorPos = -1
		}

		v.Draw(ctx, in.StartY+count)
		line := buffer.String()
		if f := cache[count]; f != line {
			io.WriteString(out, line)
			cache[count] = line
		}
		reverse = !reverse
		count++
	}
}

type MemoryCsv struct {
	Data      [][]string
	StartX    int
	StartY    int
	readCount int
}

func (M *MemoryCsv) Read() ([]string, error) {
	if M.StartY+M.readCount >= len(M.Data) {
		return nil, io.EOF
	}
	csv := M.Data[M.StartY+M.readCount]
	if M.StartX <= len(csv) {
		csv = csv[M.StartX:]
	} else {
		csv = []string{}
	}
	M.readCount++
	return csv, nil
}

const (
	_ANSI_CURSOR_OFF = "\x1B[?25l"
	_ANSI_CURSOR_ON  = "\x1B[?25h"
	_ANSI_YELLOW     = "\x1B[0;33;1m"
	_ANSI_RESET      = "\x1B[0m"
)

const (
	_KEY_CTRL_A = "\x01"
	_KEY_CTRL_B = "\x02"
	_KEY_CTRL_E = "\x05"
	_KEY_CTRL_F = "\x06"
	_KEY_CTRL_L = "\x0C"
	_KEY_CTRL_N = "\x0E"
	_KEY_CTRL_P = "\x10"
	_KEY_DOWN   = "\x1B[B"
	_KEY_ESC    = "\x1B"
	_KEY_LEFT   = "\x1B[D"
	_KEY_RIGHT  = "\x1B[C"
	_KEY_UP     = "\x1B[A"
	_KEY_F2     = "\x1B[OQ"
)

const (
	emptyDummyCode = "\uF8FF" // one of the Unicode Private Use Area
)

func cat(in io.Reader, out io.Writer) (_CodeFlag, error) {
	br := bufio.NewReader(in)
	codeFlag := nonBomUtf8
	for {
		text, err := br.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				return codeFlag, nil
			}
			return codeFlag, err
		}
		if text == "" {
			text = emptyDummyCode
		} else {
			if L := len(text); L >= 2 && text[L-2] == '\r' {
				codeFlag = codeFlag | hasCR
			}
			var codeFlag1 _CodeFlag
			text, codeFlag1 = textfilter(text)
			codeFlag = codeFlag | codeFlag1
		}
		io.WriteString(out, text)
	}
}

func getIn() (io.ReadCloser, <-chan _CodeFlag) {
	chCodeFlag := make(chan _CodeFlag, 1)
	pin, pout := io.Pipe()
	go func() {
		codeFlag, err := cat(multiFileReader(flag.Args()...), pout)
		pout.CloseWithError(err)
		chCodeFlag <- codeFlag
	}()
	return pin, chCodeFlag
}

var optionTsv = flag.Bool("t", false, "use TAB as field-separator")
var optionCsv = flag.Bool("c", false, "use Comma as field-separator")

func searchForward(csvlines [][]string, r, c int, target string) (bool, int, int) {
	c++
	for r < len(csvlines) {
		for c < len(csvlines[r]) {
			if strings.Contains(csvlines[r][c], target) {
				return true, r, c
			}
			c++
		}
		r++
		c = 0
	}
	return false, r, c
}

func searchBackward(csvlines [][]string, r, c int, target string) (bool, int, int) {
	c--
	for {
		for c >= 0 {
			if strings.Contains(csvlines[r][c], target) {
				return true, r, c
			}
			c--
		}
		r--
		if r < 0 {
			return false, r, c
		}
		c = len(csvlines[r]) - 1
	}
}

var skkInit sync.Once

func getline(out io.Writer, prompt string, defaultStr string) (string, error) {
	skkInit.Do(func() {
		env := os.Getenv("GOREADLINESKK")
		if env != "" {
			_, err := skk.Config{
				MiniBuffer: skk.MiniBufferOnCurrentLine{},
			}.SetupWithString(env)
			if err != nil {
				fmt.Fprintln(os.Stderr, err.Error())
			}
		}
	})

	editor := readline.Editor{
		Writer:  out,
		Default: defaultStr,
		Cursor:  65535,
		PromptWriter: func(w io.Writer) (int, error) {
			return fmt.Fprintf(w, "\r\x1B[0;33;40;1m%s%s", prompt, ERASE_LINE)
		},
		LineFeedWriter: func(readline.Result, io.Writer) (int, error) {
			return 0, nil
		},
		Coloring: &skk.Coloring{},
	}
	defer io.WriteString(out, _ANSI_CURSOR_OFF)
	editor.BindKey(keys.Escape, readline.CmdInterrupt)
	return editor.ReadLine(context.Background())
}

type WriteNopCloser struct {
	io.Writer
}

func (*WriteNopCloser) Close() error {
	return nil
}

var overWritten = map[string]struct{}{}

func yesNo(tty1 *tty.TTY, out io.Writer, message string) bool {
	fmt.Fprintf(out, "%s\r%s%s", _ANSI_YELLOW, message, ERASE_LINE)
	ch, err := readline.GetKey(tty1)
	return err == nil && ch == "y"
}

func writeCsvTo(csvlines [][]string, comma rune, codeFlag _CodeFlag, fd io.Writer) {
	if (codeFlag & isAnsi) != 0 {
		pipeIn, pipeOut := io.Pipe()
		go func() {
			w := csv.NewWriter(pipeOut)
			w.Comma = comma
			w.UseCRLF = true
			w.WriteAll(csvlines)
			w.Flush()
			pipeOut.Close()
		}()
		sc := bufio.NewScanner(pipeIn)
		bw := bufio.NewWriter(fd)
		for sc.Scan() {
			bytes, _ := mbcs.Utf8ToAnsi(sc.Text(), mbcs.ACP)
			bw.Write(bytes)
			bw.WriteByte('\r')
			bw.WriteByte('\n')
		}
		bw.Flush()
	} else {
		if (codeFlag & hasBom) != 0 {
			io.WriteString(fd, "\uFEFF")
		}
		w := csv.NewWriter(fd)
		w.Comma = comma
		w.UseCRLF = true
		w.WriteAll(csvlines)
		w.Flush()
	}
}

func first[T any](value T, _ error) T {
	return value
}

func mains() error {
	ctx := context.Background()

	fmt.Printf("lispread %s-%s-%s by %s\n",
		version, runtime.GOOS, runtime.GOARCH, runtime.Version())

	disable := colorable.EnableColorsStdout(nil)
	if disable != nil {
		defer disable()
	}
	out := colorable.NewColorableStdout()

	io.WriteString(out, _ANSI_CURSOR_OFF)
	defer io.WriteString(out, _ANSI_CURSOR_ON)
	var chCodeFlag <-chan _CodeFlag

	var csvlines [][]string
	var fieldSeperator rune
	if len(flag.Args()) <= 0 && term.IsTerminal(int(os.Stdin.Fd())) {
		csvlines = [][]string{[]string{""}}
		fieldSeperator = '\t'
	} else {
		var pin io.ReadCloser

		pin, chCodeFlag = getIn()
		defer pin.Close()

		in := csv.NewReader(pin)
		in.FieldsPerRecord = -1
		args := flag.Args()
		if len(args) >= 1 && !strings.HasSuffix(strings.ToLower(args[0]), ".csv") {
			in.Comma = '\t'
		}
		if *optionTsv {
			in.Comma = '\t'
		}
		if *optionCsv {
			in.Comma = ','
		}
		fieldSeperator = in.Comma
		var err error
		csvlines, err = readCsvAll(in)
		if err != nil {
			return err
		}
		if len(csvlines) <= 0 {
			return io.EOF
		}
	}
	tty1, err := tty.Open()
	if err != nil {
		return err
	}

	defer tty1.Close()

	colIndex := 0
	rowIndex := 0
	startRow := 0
	startCol := 0

	lastSearch := searchForward
	lastSearchRev := searchBackward
	lastWord := ""
	var lastWidth, lastHeight int

	message := ""
	codeFlag := nonBomUtf8
	var killbuffer string
	for {
		screenWidth, screenHeight, err := tty1.Size()
		if err != nil {
			return err
		}
		if lastWidth != screenWidth || lastHeight != screenHeight {
			cache = map[int]string{}
			lastWidth = screenWidth
			lastHeight = screenHeight
			io.WriteString(out, _ANSI_CURSOR_OFF)
		}
		cols := (screenWidth - 1) / CELL_WIDTH

		window := &MemoryCsv{Data: csvlines, StartX: startCol, StartY: startRow}
		refCnt := map[[2]int]struct{}{}
		referf := func(ctx context.Context, w *gmnlisp.World, absrow int, abscol int) (string, error) {
			// row and col start from 1 not 0.
			if absrow < 0 || absrow >= len(csvlines) {
				return "", fmt.Errorf("not found row(%d)", absrow)
			}
			theRow := csvlines[absrow]
			if abscol < 0 || abscol >= len(theRow) {
				return "", fmt.Errorf("not found cell(%d)", abscol)
			}
			cell := theRow[abscol]
			if !strings.HasPrefix(cell, "(") {
				return cell, nil
			}
			key := [2]int{absrow, abscol}
			if _, ok := refCnt[key]; ok {
				return "", fmt.Errorf("circular reference")
			}
			refCnt[key] = struct{}{}

			dynamics := w.NewDynamics()
			defer dynamics.Close()
			dynamics.Set(rowSymbol(), gmnlisp.Integer(absrow))
			dynamics.Set(colSymbol(), gmnlisp.Integer(abscol))

			val, err := w.Interpret(ctx, cell)

			delete(refCnt, key)
			if val == nil {
				return "", err
			}
			return val.String(), err
		}
		lf, err := view(ctx, window, colIndex-startCol, rowIndex-startRow, screenWidth-1, screenHeight-1, referf, out)
		if err != nil {
			return err
		}
		fmt.Fprintln(out, "\r") // \r is for Linux & go-tty
		lf++

		if chCodeFlag != nil {
			select {
			case val := <-chCodeFlag:
				codeFlag = val
			default:
			}
		}

		io.WriteString(out, _ANSI_YELLOW)
		if message != "" {
			io.WriteString(out, runewidth.Truncate(message, screenWidth-1, ""))
			message = ""
		} else if 0 <= rowIndex && rowIndex < len(csvlines) {
			n := 0
			if fieldSeperator == '\t' {
				n += first(io.WriteString(out, "[TSV]"))
			} else if fieldSeperator == ',' {
				n += first(io.WriteString(out, "[CSV]"))
			}
			if (codeFlag & hasCR) != 0 {
				n += first(io.WriteString(out, "[CRLF]"))
			} else {
				n += first(io.WriteString(out, "[LF]"))
			}
			if (codeFlag & hasBom) != 0 {
				n += first(io.WriteString(out, "[BOM]"))
			}
			if (codeFlag & isAnsi) != 0 {
				n += first(io.WriteString(out, "[ANSI]"))
			}
			if 0 <= colIndex && colIndex < len(csvlines[rowIndex]) {
				n += first(fmt.Fprintf(out, "(%d,%d):",
					rowIndex+1,
					colIndex+1))

				io.WriteString(out, runewidth.Truncate(replaceTable.Replace(csvlines[rowIndex][colIndex]), screenWidth-n, "..."))
			}
		}
		io.WriteString(out, _ANSI_RESET)
		io.WriteString(out, ERASE_SCRN_AFTER)

		ch, err := readline.GetKey(tty1)
		if err != nil {
			return err
		}

		getline := func(out io.Writer, prompt string, defaultStr string) (string, error) {
			text, err := getline(out, prompt, defaultStr)
			clear(cache)
			return text, err
		}

		switch ch {
		case _KEY_CTRL_L:
			clear(cache)
		case "q", _KEY_ESC:
			io.WriteString(out, _ANSI_YELLOW+"\rQuit Sure ? [y/n]"+ERASE_LINE)
			if ch, err := readline.GetKey(tty1); err == nil && ch == "y" {
				io.WriteString(out, "\n")
				return nil
			}
		case "j", _KEY_DOWN, _KEY_CTRL_N:
			if rowIndex < len(csvlines)-1 {
				rowIndex++
			}
		case "k", _KEY_UP, _KEY_CTRL_P:
			if rowIndex > 0 {
				rowIndex--
			}
		case "h", _KEY_LEFT, _KEY_CTRL_B:
			if colIndex > 0 {
				colIndex--
			}
		case "l", _KEY_RIGHT, _KEY_CTRL_F:
			colIndex++
		case "0", "^", _KEY_CTRL_A:
			colIndex = 0
		case "$", _KEY_CTRL_E:
			colIndex = len(csvlines[rowIndex]) - 1
		case "<":
			rowIndex = 0
		case ">":
			rowIndex = len(csvlines) - 1
		case "n":
			if lastWord == "" {
				break
			}
			found, r, c := lastSearch(csvlines, rowIndex, colIndex, lastWord)
			if !found {
				message = fmt.Sprintf("%s: not found", lastWord)
				break
			}
			rowIndex = r
			colIndex = c
		case "N":
			if lastWord == "" {
				break
			}
			found, r, c := lastSearchRev(csvlines, rowIndex, colIndex, lastWord)
			if !found {
				message = fmt.Sprintf("%s: not found", lastWord)
				break
			}
			rowIndex = r
			colIndex = c
		case "/", "?":
			var err error
			lastWord, err = getline(out, ch, "")
			if err != nil {
				if err != readline.CtrlC {
					message = err.Error()
				}
				break
			}
			if ch == "/" {
				lastSearch = searchForward
				lastSearchRev = searchBackward
			} else {
				lastSearch = searchBackward
				lastSearchRev = searchForward
			}
			found, r, c := lastSearch(csvlines, rowIndex, colIndex, lastWord)
			if !found {
				message = fmt.Sprintf("%s: not found", lastWord)
				break
			}
			rowIndex = r
			colIndex = c
		case "o":
			if rowIndex > len(csvlines)-1 {
				break
			}
			rowIndex++
			fallthrough
		case "O":
			csvlines = append(csvlines, []string{})
			if len(csvlines) >= rowIndex+1 {
				copy(csvlines[rowIndex+1:], csvlines[rowIndex:])
				text, _ := getline(out, "new line>", "")
				csvlines[rowIndex] = []string{text}
			}
		case "D":
			if len(csvlines) <= 1 {
				break
			}
			copy(csvlines[rowIndex:], csvlines[rowIndex+1:])
			csvlines = csvlines[:len(csvlines)-1]
			if rowIndex >= len(csvlines) {
				rowIndex--
			}
		case "i":
			text, err := getline(out, "insert cell>", "")
			if err != nil {
				break
			}
			csvlines[rowIndex] = append(csvlines[rowIndex], "")
			copy(csvlines[rowIndex][colIndex+1:], csvlines[rowIndex][colIndex:])
			csvlines[rowIndex][colIndex] = text
			colIndex++
		case "a":
			text, err := getline(out, "append cell>", "")
			if err != nil {
				break
			}
			csvlines[rowIndex] = append(csvlines[rowIndex], "")
			colIndex++
			copy(csvlines[rowIndex][colIndex+1:], csvlines[rowIndex][colIndex:])
			csvlines[rowIndex][colIndex] = text
		case "r", "R", _KEY_F2:
			text, err := getline(out, "replace cell>", csvlines[rowIndex][colIndex])
			if err != nil {
				break
			}
			csvlines[rowIndex][colIndex] = text
		case "y":
			killbuffer = csvlines[rowIndex][colIndex]
			message = "yanked the current cell: " + killbuffer
		case "p":
			csvlines[rowIndex][colIndex] = killbuffer
			message = "pasted: " + killbuffer
		case "d", "x":
			if len(csvlines[rowIndex]) <= 1 {
				csvlines[rowIndex][0] = ""
			} else {
				copy(csvlines[rowIndex][colIndex:], csvlines[rowIndex][colIndex+1:])
				csvlines[rowIndex] = csvlines[rowIndex][:len(csvlines[rowIndex])-1]
			}
		case "w":
			fname := "-"
			var err error
			if args := flag.Args(); len(args) >= 1 {
				fname, err = filepath.Abs(args[0])
				if err != nil {
					message = err.Error()
					break
				}
			}
			fname, err = getline(out, "write to>", fname)
			if err != nil {
				break
			}
			var fd io.WriteCloser
			if fname == "-" {
				fd = &WriteNopCloser{Writer: os.Stdout}
			} else {
				fd, err = os.OpenFile(fname, os.O_WRONLY|os.O_EXCL|os.O_CREATE, 0666)
				if os.IsExist(err) {
					if _, ok := overWritten[fname]; ok {
						os.Remove(fname)
					} else {
						if !yesNo(tty1, out, "Overwrite as \""+fname+"\" [y/n] ?") {
							break
						}
						backupName := fname + "~"
						os.Remove(backupName)
						os.Rename(fname, backupName)
						overWritten[fname] = struct{}{}
					}
					fd, err = os.OpenFile(fname, os.O_WRONLY|os.O_EXCL|os.O_CREATE, 0666)
				}
				if err != nil {
					message = err.Error()
					break
				}
			}

			writeCsvTo(csvlines, fieldSeperator, codeFlag, fd)
			fd.Close()
		}
		if colIndex >= len(csvlines[rowIndex]) {
			colIndex = len(csvlines[rowIndex]) - 1
		}

		if rowIndex < startRow {
			startRow = rowIndex
		} else if rowIndex >= startRow+screenHeight-1 {
			startRow = rowIndex - (screenHeight - 1) + 1
		}
		if colIndex < startCol {
			startCol = colIndex
		} else if colIndex >= startCol+cols {
			startCol = colIndex - cols + 1
		}

		if lf > 0 {
			fmt.Fprintf(out, "\r\x1B[%dA", lf)
		} else {
			fmt.Fprint(out, "\r")
		}
	}
}

func main() {
	flag.Parse()
	if err := mains(); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
}
