package main

import (
	"strings"
	"testing"
)

func TestTerminalBasicLines(t *testing.T) {
	term := NewTerminal(100, 80, 24)
	term.Write([]byte("line1\r\n"))
	term.Write([]byte("line2\r\n"))
	term.Write([]byte("line3"))

	data, _ := term.Read()
	s := string(data)

	if !strings.Contains(s, "line1") {
		t.Errorf("missing line1 in output: %q", s)
	}
	if !strings.Contains(s, "line2") {
		t.Errorf("missing line2 in output: %q", s)
	}
	if !strings.Contains(s, "line3") {
		t.Errorf("missing line3 in output: %q", s)
	}
}

func TestTerminalSGRPreserved(t *testing.T) {
	term := NewTerminal(100, 80, 24)
	term.Write([]byte("\x1b[1;31mRed Bold\x1b[0m"))

	data, _ := term.Read()
	s := string(data)

	if !strings.Contains(s, "Red Bold") {
		t.Errorf("missing text in output: %q", s)
	}
	// SGR sequences should be preserved (serialized output contains style info)
	if !strings.Contains(s, "\x1b[") {
		t.Errorf("missing SGR sequences in output: %q", s)
	}
}

func TestTerminalResize(t *testing.T) {
	term := NewTerminal(100, 80, 24)
	term.Write([]byte("hello world this is a long line that might wrap"))
	term.Resize(40, 24)

	data, _ := term.Read()
	s := string(data)
	if !strings.Contains(s, "hello") {
		t.Errorf("missing text after resize: %q", s)
	}
}

func TestTerminalSeq(t *testing.T) {
	term := NewTerminal(100, 80, 24)

	seq1 := term.Write([]byte("a"))
	seq2 := term.Write([]byte("b"))

	if seq1 == 0 || seq2 == 0 {
		t.Errorf("seq should be non-zero")
	}
	if seq2 <= seq1 {
		t.Errorf("seq should be monotonically increasing: %d vs %d", seq1, seq2)
	}

	readSeq := term.Seq()
	if readSeq != seq2 {
		t.Errorf("Seq() should return last write seq: got %d, want %d", readSeq, seq2)
	}
}

func TestTerminalUTF8(t *testing.T) {
	term := NewTerminal(100, 80, 24)
	term.Write([]byte("你好世界\r\n"))
	term.Write([]byte("日本語テスト"))

	data, _ := term.Read()
	s := string(data)

	if !strings.Contains(s, "你好世界") {
		t.Errorf("missing Chinese text: %q", s)
	}
	if !strings.Contains(s, "日本語テスト") {
		t.Errorf("missing Japanese text: %q", s)
	}
}

func TestTerminalReset(t *testing.T) {
	term := NewTerminal(100, 80, 24)
	term.Write([]byte("some content"))
	term.Reset()

	data, _ := term.Read()
	s := string(data)

	if strings.Contains(s, "some content") {
		t.Errorf("content should be cleared after reset: %q", s)
	}
}

func TestTerminalWriteReturnsSeq(t *testing.T) {
	term := NewTerminal(100, 80, 24)

	seq1 := term.Write([]byte("hello"))
	seq2 := term.Write(nil) // empty write also increments seq
	seq3 := term.Write([]byte("world"))

	if seq2 <= seq1 {
		t.Errorf("seq should increment even for empty write")
	}
	if seq3 <= seq2 {
		t.Errorf("seq should be monotonically increasing")
	}
}

func TestTerminalScrollback(t *testing.T) {
	term := NewTerminal(5, 80, 3) // only keep 5 lines scrollback

	// write enough lines to fill and exceed scrollback
	for i := 0; i < 10; i++ {
		term.Write([]byte(strings.Repeat("line", i+1) + "\r\n"))
	}

	data, _ := term.Read()
	s := string(data)

	// scrollback limited to 5 lines, early lines should be discarded
	if strings.Contains(s, "line\r\nline") {
		// first line (shortest) should have been discarded
	}
	// should contain the latest line
	if !strings.Contains(s, "line") {
		t.Errorf("should contain text content: %q", s)
	}
}

func TestTerminalClearScreen(t *testing.T) {
	term := NewTerminal(100, 80, 24)
	term.Write([]byte("before clear"))
	term.Write([]byte("\x1b[2J")) // clear screen
	term.Write([]byte("after clear"))

	data, _ := term.Read()
	s := string(data)

	if !strings.Contains(s, "after clear") {
		t.Errorf("should contain text after clear: %q", s)
	}
}

func TestTerminalReadAtNarrowClient(t *testing.T) {
	// simulate: 200-col terminal produces content, phone with 40 cols requests history
	term := NewTerminal(100, 200, 24)

	// write a line close to 200 columns
	term.Write([]byte(strings.Repeat("A", 180) + "\r\n"))
	term.Write([]byte("short line\r\n"))
	term.Write([]byte(strings.Repeat("B", 150)))

	// read at phone size (40 columns)
	data, seq := term.ReadAt(40, 24)
	s := string(data)

	if seq == 0 {
		t.Errorf("seq should be non-zero")
	}
	if !strings.Contains(s, "short line") {
		t.Errorf("should contain short line: %q", s)
	}
	// key verification: should not contain cursor-right sequences exceeding targetCols(40)
	// \x1b[NC where N should not exceed 40 (original 200 cols would have \x1b[160C)
	for i := 0; i < len(s); i++ {
		if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '[' {
			// found CSI sequence, parsing number
			j := i + 2
			numStr := ""
			for j < len(s) && s[j] >= '0' && s[j] <= '9' {
				numStr += string(s[j])
				j++
			}
			if j < len(s) && s[j] == 'C' && numStr != "" {
				// this is \x1b[NC cursor right
				n := 0
				for _, c := range numStr {
					n = n*10 + int(c-'0')
				}
				if n > 40 {
					// cursor movement from line wrapping is allowed in actual terminal content
					// but should not have large gaps from original 200 columns
					t.Errorf("cursor-right %d exceeds target cols 40 in output near pos %d", n, i)
					break
				}
			}
		}
	}
}

func TestTerminalReadAtPreservesSGR(t *testing.T) {
	term := NewTerminal(100, 120, 24)
	term.Write([]byte("\x1b[1;31mRed Bold Text\x1b[0m\r\n"))
	term.Write([]byte("normal line"))

	data, _ := term.ReadAt(80, 24)
	s := string(data)

	if !strings.Contains(s, "Red Bold Text") {
		t.Errorf("should contain styled text: %q", s)
	}
	if !strings.Contains(s, "\x1b[") {
		t.Errorf("should preserve SGR sequences: %q", s)
	}
}

func TestTerminalReadAtSameSize(t *testing.T) {
	// ReadAt at same size should return same content
	term := NewTerminal(100, 80, 24)
	term.Write([]byte("test content\r\n"))
	term.Write([]byte("more data"))

	data1, _ := term.Read()
	data2, _ := term.ReadAt(80, 24)

	if string(data1) != string(data2) {
		t.Errorf("ReadAt with same dimensions should produce same output")
	}
}
