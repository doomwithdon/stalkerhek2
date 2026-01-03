package hls

import (
	"bufio"
	"io"
	"net/url"
	"regexp"
	"strings"
)

func deleteAfterLastSlash(str string) string {
	return str[0 : strings.LastIndex(str, "/")+1]
}

var reURILinkExtract = regexp.MustCompile(`URI="([^"]*)"`)

func rewriteLinks(rbody *io.ReadCloser, prefix, linkRoot string) string {
	var sb strings.Builder
	scanner := bufio.NewScanner(*rbody)
	linkRootURL, _ := url.Parse(linkRoot) // It will act as a base URL for full URLs

	modifyLink := func(link string) string {
		var l string

		switch {
		case strings.HasPrefix(link, "//"):
			tmpURL, _ := url.Parse(link)
			tmp2URL, _ := url.Parse(tmpURL.RequestURI())
			link = (linkRootURL.ResolveReference(tmp2URL)).String()
			l = strings.ReplaceAll(link, linkRoot, "")
		case strings.HasPrefix(link, "/"):
			tmp2URL, _ := url.Parse(link)
			link = (linkRootURL.ResolveReference(tmp2URL)).String()
			l = strings.ReplaceAll(link, linkRoot, "")
		default:
			l = link
		}

		newurl, _ := url.Parse(prefix + l)
		return newurl.RequestURI()
	}

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "#") {
			line = modifyLink(line)
		} else if strings.Contains(line, "URI=\"") && !strings.Contains(line, "URI=\"\"") {
			link := reURILinkExtract.FindStringSubmatch(line)[1]
			line = reURILinkExtract.ReplaceAllString(line, `URI="`+modifyLink(link)+`"`)
		}
		sb.WriteString(line)
		sb.WriteByte('\n')
	}

	return applyPlaylistDelay(sb.String())
}

func applyPlaylistDelay(content string) string {
	n := getPlaylistDelaySegments()
	if n <= 0 {
		return content
	}
	lines := strings.Split(content, "\n")
	// Collect header lines and segment blocks.
	head := make([]string, 0, 32)
	buf := make([]string, 0, 8)
	segments := make([][]string, 0, 16)
	seenFirstSegment := false
	for _, ln := range lines {
		if ln == "" {
			continue
		}
		if strings.HasPrefix(ln, "#") {
			if !seenFirstSegment {
				head = append(head, ln)
			} else {
				buf = append(buf, ln)
			}
			continue
		}
		// URI line
		seenFirstSegment = true
		buf = append(buf, ln)
		seg := make([]string, len(buf))
		copy(seg, buf)
		segments = append(segments, seg)
		buf = buf[:0]
	}
	if len(segments) == 0 {
		return content
	}
	if n >= len(segments) {
		// Keep at least 1 segment.
		segments = segments[:1]
	} else {
		segments = segments[:len(segments)-n]
	}
	var out strings.Builder
	for _, ln := range head {
		out.WriteString(ln)
		out.WriteByte('\n')
	}
	for _, seg := range segments {
		for _, ln := range seg {
			out.WriteString(ln)
			out.WriteByte('\n')
		}
	}
	return out.String()
}
