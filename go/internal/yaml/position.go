package yaml

// ErrorPosition returns the last decoding error's byte offset when the
// reader rejected input, or -1 and a one-based line and character column
// for scanner, parser and alias errors. It reads the same marks used by
// the decoder, before its public error discards the column information.
func (dec *Decoder) ErrorPosition() (offset, line, column int) {
	p := dec.parser
	if p.parser.error == yaml_READER_ERROR {
		return p.parser.problem_offset, 0, 0
	}
	mark := p.parser.problem_mark
	if p.parser.error == yaml_NO_ERROR {
		mark = p.event.start_mark
	}
	return -1, mark.line + 1, mark.column + 1
}
