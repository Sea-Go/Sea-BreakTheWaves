package processing

import (
	"archive/zip"
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/blob"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/item"
	"golang.org/x/net/html"
	"io"
	"net/url"
	"os"
	"path"
	"strings"
	"unicode/utf8"
)

var ErrUnsupported = errors.New("unsupported or protected document")

type Parsed struct {
	Navigation []item.Navigation
	Sections   []item.Section
	TextKey    string
	Digest     string
}
type Parser struct {
	Objects         blob.Store
	MaxUncompressed int64
	Attempt         string
}
type sectionWriter struct {
	ctx          context.Context
	store        blob.Store
	rev          string
	file         *os.File
	full         *os.File
	title, path  string
	ordinal      int
	sections     []item.Section
	digest       io.Writer
	bytes        int64
	max          int64
	runes        int64
	sectionBytes int64
	anchors      map[string]int64
	seek         []item.SeekPoint
	navigation   []item.Navigation
}

func (w *sectionWriter) start(title, p string) error {
	if w.file != nil {
		if e := w.finish(); e != nil {
			return e
		}
	}
	f, e := os.CreateTemp("", "btw-section-")
	if e != nil {
		return e
	}
	w.file = f
	w.title = title
	w.path = p
	w.ordinal++
	w.runes = 0
	w.sectionBytes = 0
	w.anchors = map[string]int64{}
	w.seek = []item.SeekPoint{{Rune: 0, Byte: 0}}
	return nil
}
func (w *sectionWriter) write(s string) error {
	if !utf8.ValidString(s) {
		return errors.New("source is not valid UTF-8")
	}
	for pos, r := range s {
		if w.runes > 0 && w.runes%4096 == 0 {
			w.seek = append(w.seek, item.SeekPoint{Rune: w.runes, Byte: w.sectionBytes + int64(pos)})
		}
		w.runes++
		_ = r
	}
	w.sectionBytes += int64(len(s))
	w.bytes += int64(len(s))
	if w.bytes > w.max {
		return errors.New("uncompressed document exceeds configured limit")
	}
	if e := w.ctx.Err(); e != nil {
		return e
	}
	_, e := io.WriteString(io.MultiWriter(w.file, w.full, w.digest), s)
	return e
}
func (w *sectionWriter) finish() error {
	if w.file == nil {
		return nil
	}
	f := w.file
	w.file = nil
	defer os.Remove(f.Name())
	defer f.Close()
	n, e := f.Seek(0, io.SeekEnd)
	if e != nil {
		return e
	}
	if _, e = f.Seek(0, io.SeekStart); e != nil {
		return e
	}
	id := fmt.Sprintf("s%08d", w.ordinal)
	key := fmt.Sprintf("sources/%s/sections/%s.txt", w.rev, id)
	// Parsing can be retried: the already committed structure is reused by the worker.
	if _, e = w.store.Put(w.ctx, key, f, n, "text/plain; charset=utf-8"); e != nil {
		return e
	}
	w.sections = append(w.sections, item.Section{ID: id, Order: w.ordinal, Title: w.title, Path: w.path, ObjectKey: key, Bytes: n, Runes: w.runes, Anchors: w.anchors, Seek: w.seek})
	return nil
}
func (p Parser) Parse(ctx context.Context, r item.Revision) (out Parsed, err error) {
	if p.MaxUncompressed <= 0 {
		return out, errors.New("parser limit is required")
	}
	full, e := os.CreateTemp("", "btw-text-")
	if e != nil {
		return out, e
	}
	defer os.Remove(full.Name())
	defer full.Close()
	hash := sha256.New()
	w := &sectionWriter{ctx: ctx, store: p.Objects, rev: r.ID + "/" + p.Attempt, full: full, digest: hash, max: p.MaxUncompressed}
	defer func() {
		if w.file != nil {
			w.file.Close()
			os.Remove(w.file.Name())
		}
	}()
	input, e := p.Objects.Open(ctx, r.FileKey, 0, -1)
	if e != nil {
		return out, e
	}
	defer input.Close()
	switch strings.Split(r.MediaType, ";")[0] {
	case "application/epub+zip":
		f, e := os.CreateTemp("", "btw-epub-")
		if e != nil {
			return out, e
		}
		defer os.Remove(f.Name())
		defer f.Close()
		n, e := io.Copy(f, input)
		if e != nil {
			return out, e
		}
		z, e := zip.NewReader(f, n)
		if e != nil {
			return out, e
		}
		e = parseEPUB(z, w)
		if e != nil {
			return out, e
		}
	case "text/plain", "text/markdown", "text/html":
		if e = w.start(r.Title, "body"); e != nil {
			return out, e
		}
		if strings.HasPrefix(r.MediaType, "text/html") {
			e = writeHTML(input, w)
		} else {
			e = writeText(input, w, strings.HasPrefix(r.MediaType, "text/markdown"))
		}
		if e != nil {
			return out, e
		}
	default:
		return out, ErrUnsupported
	}
	if e = w.finish(); e != nil {
		return out, e
	}
	if len(w.sections) == 0 || w.bytes == 0 {
		return out, errors.New("document contains no readable text")
	}
	n, e := full.Seek(0, io.SeekEnd)
	if e != nil {
		return out, e
	}
	if _, e = full.Seek(0, 0); e != nil {
		return out, e
	}
	key := "sources/" + r.ID + "/" + p.Attempt + "/text.txt"
	if _, e = p.Objects.Put(ctx, key, full, n, "text/plain; charset=utf-8"); e != nil {
		return out, e
	}
	for i := range w.navigation {
		target := strings.SplitN(w.navigation[i].Href, "#", 2)
		for _, section := range w.sections {
			if section.Path == target[0] {
				w.navigation[i].SectionID = section.ID
				if len(target) == 2 {
					w.navigation[i].Start = section.Anchors[target[1]]
				}
				break
			}
		}
	}
	return Parsed{Sections: w.sections, Navigation: w.navigation, TextKey: key, Digest: hex.EncodeToString(hash.Sum(nil))}, nil
}
func writeText(in io.Reader, w *sectionWriter, markdown bool) error {
	r := bufio.NewReaderSize(in, 64*1024)
	lineStart := true
	first := true
	carry := ""
	for {
		fragment, e := r.ReadSlice('\n')
		part := carry + string(fragment)
		carry = ""
		// A physical line may exceed memory budgets. Keep only an incomplete
		// UTF-8 suffix; fragments are computation buffers, never stored items.
		if errors.Is(e, bufio.ErrBufferFull) && len(part) > 0 {
			last := len(part) - 1
			for last > 0 && !utf8.RuneStart(part[last]) {
				last--
			}
			if !utf8.FullRuneInString(part[last:]) {
				carry, part = part[last:], part[:last]
			}
		}
		if first {
			part = strings.TrimPrefix(part, "\ufeff")
			first = false
		}
		if len(part) > 0 {
			if markdown && lineStart {
				trim := strings.TrimLeft(part, "#")
				if len(trim) < len(part) && strings.HasPrefix(trim, " ") {
					title := strings.TrimSpace(trim)
					// Heading metadata is bounded; the complete heading remains in text.
					if len([]rune(title)) > 1024 {
						title = string([]rune(title)[:1024])
					}
					if len(w.sections) > 0 || w.bytes > 0 {
						if x := w.start(title, fmt.Sprintf("heading/%d", w.ordinal+1)); x != nil {
							return x
						}
					} else {
						w.title = title
					}
				}
			}
			if x := w.write(part); x != nil {
				return x
			}
			lineStart = strings.HasSuffix(part, "\n")
		}
		if errors.Is(e, io.EOF) {
			return nil
		}
		if e != nil && !errors.Is(e, bufio.ErrBufferFull) {
			return e
		}
	}
}
func writeHTML(in io.Reader, w *sectionWriter) error {
	z := html.NewTokenizer(in)
	skip := 0
	for {
		switch z.Next() {
		case html.ErrorToken:
			if errors.Is(z.Err(), io.EOF) {
				return nil
			}
			return z.Err()
		case html.StartTagToken, html.SelfClosingTagToken:
			token := z.Token()
			n := token.Data
			for _, a := range token.Attr {
				if a.Key == "id" || a.Key == "xml:id" {
					w.anchors[a.Val] = w.runes
				}
			}
			if n == "script" || n == "style" {
				skip++
			}
			if skip == 0 && (n == "p" || n == "div" || n == "br" || n == "li" || strings.HasPrefix(n, "h")) {
				if e := w.write("\n"); e != nil {
					return e
				}
			}
		case html.EndTagToken:
			name, _ := z.TagName()
			n := string(name)
			if (n == "script" || n == "style") && skip > 0 {
				skip--
			}
			if skip == 0 && (n == "p" || n == "div" || n == "li") {
				if e := w.write("\n"); e != nil {
					return e
				}
			}
		case html.TextToken:
			if skip == 0 {
				if e := w.write(string(z.Text())); e != nil {
					return e
				}
			}
		}
	}
}
func parseEPUB(z *zip.Reader, w *sectionWriter) error {
	files := map[string]*zip.File{}
	var total uint64
	for _, f := range z.File {
		if f.FileInfo().IsDir() {
			continue
		}
		if strings.HasPrefix(f.Name, "/") || path.Clean(f.Name) != f.Name || strings.HasPrefix(f.Name, "../") {
			return errors.New("unsafe EPUB path")
		}
		files[f.Name] = f
		total += f.UncompressedSize64
		if total > uint64(w.max) {
			return errors.New("EPUB expansion exceeds configured limit")
		}
	}
	if encrypted, ok := files["META-INF/encryption.xml"]; ok {
		r, e := encrypted.Open()
		if e != nil {
			return e
		}
		decoder := xml.NewDecoder(r)
		for {
			token, e := decoder.Token()
			if errors.Is(e, io.EOF) {
				break
			}
			if e != nil {
				r.Close()
				return e
			}
			if start, ok := token.(xml.StartElement); ok && start.Name.Local == "CipherReference" {
				for _, attr := range start.Attr {
					if attr.Name.Local == "URI" {
						ext := strings.ToLower(path.Ext(attr.Value))
						if ext != ".otf" && ext != ".ttf" && ext != ".woff" && ext != ".woff2" {
							r.Close()
							return ErrUnsupported
						}
					}
				}
			}
		}
		r.Close()
	}
	open := func(name string) (io.ReadCloser, error) {
		f, ok := files[name]
		if !ok {
			return nil, fmt.Errorf("missing EPUB member %s", name)
		}
		return f.Open()
	}
	rc, e := open("META-INF/container.xml")
	if e != nil {
		return e
	}
	var container struct {
		Roots []struct {
			Path string `xml:"full-path,attr"`
		} `xml:"rootfiles>rootfile"`
	}
	e = xml.NewDecoder(rc).Decode(&container)
	rc.Close()
	if e != nil || len(container.Roots) == 0 {
		return errors.New("invalid EPUB container")
	}
	packagePath := container.Roots[0].Path
	rc, e = open(packagePath)
	if e != nil {
		return e
	}
	var book struct {
		Manifest []struct {
			ID         string `xml:"id,attr"`
			Href       string `xml:"href,attr"`
			Media      string `xml:"media-type,attr"`
			Properties string `xml:"properties,attr"`
		} `xml:"manifest>item"`
		Spine []struct {
			Ref    string `xml:"idref,attr"`
			Linear string `xml:"linear,attr"`
		} `xml:"spine>itemref"`
	}
	e = xml.NewDecoder(rc).Decode(&book)
	rc.Close()
	if e != nil {
		return e
	}
	if len(book.Spine) == 0 {
		return errors.New("EPUB reading order is empty")
	}
	base := path.Dir(packagePath)
	hrefs := map[string]string{}
	titles := map[string]string{}
	hasNav := false
	for _, m := range book.Manifest {
		if strings.Contains(m.Properties, "nav") {
			hasNav = true
		}
	}
	for _, m := range book.Manifest {
		resolved, err := resolveEPUB(base, m.Href)
		if err != nil {
			return err
		}
		hrefs[m.ID] = strings.Split(resolved, "#")[0]
		if strings.Contains(m.Properties, "nav") || (!hasNav && m.Media == "application/x-dtbncx+xml") {
			rc, e := open(hrefs[m.ID])
			if e != nil {
				return e
			}
			navigation, err := readNavigation(rc, path.Dir(hrefs[m.ID]))
			if err != nil {
				rc.Close()
				return err
			}
			w.navigation = append(w.navigation, navigation...)
			for _, entry := range navigation {
				name := strings.Split(entry.Href, "#")[0]
				if titles[name] == "" {
					titles[name] = entry.Title
				}
			}
			rc.Close()
		}
	}
	for _, entry := range book.Spine {
		name := hrefs[entry.Ref]
		if name == "" {
			return errors.New("EPUB spine references missing manifest item")
		}
		rc, e := open(name)
		if e != nil {
			return e
		}
		title := titles[name]
		if title == "" {
			title = path.Base(name)
		}
		e = w.start(title, name)
		if e == nil {
			e = writeHTML(rc, w)
		}
		rc.Close()
		if e != nil {
			return e
		}
	}
	return nil
}
func readNavigation(in io.Reader, base string) ([]item.Navigation, error) {
	doc, e := html.Parse(in)
	if e != nil {
		return nil, e
	}
	out := []item.Navigation{}
	attribute := func(n *html.Node, key string) string {
		for _, a := range n.Attr {
			if a.Key == key {
				return a.Val
			}
		}
		return ""
	}
	var text func(*html.Node) string
	text = func(n *html.Node) string {
		if n.Type == html.TextNode {
			return n.Data
		}
		var b strings.Builder
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			b.WriteString(text(c))
		}
		return b.String()
	}
	var walk func(*html.Node, string, int) error
	walk = func(n *html.Node, parent string, depth int) error {
		if depth > 128 {
			return errors.New("navigation nesting too deep")
		}
		next := parent
		if n.Type == html.ElementNode && (n.Data == "li" || n.Data == "navpoint") {
			var target *html.Node
			var find func(*html.Node)
			find = func(v *html.Node) {
				if target != nil {
					return
				}
				for c := v.FirstChild; c != nil; c = c.NextSibling {
					if c.Type == html.ElementNode && (c.Data == "li" || c.Data == "navpoint") {
						continue
					}
					if c.Type == html.ElementNode && (c.Data == "a" || c.Data == "content") {
						target = c
						return
					}
					find(c)
				}
			}
			find(n)
			if target != nil {
				href := attribute(target, "href")
				if href == "" {
					href = attribute(target, "src")
				}
				if href != "" {
					title := strings.TrimSpace(text(target))
					if title == "" {
						for c := n.FirstChild; c != nil; c = c.NextSibling {
							if c.Data == "navlabel" {
								title = strings.TrimSpace(text(c))
								break
							}
						}
					}
					resolved, err := resolveEPUB(base, href)

					if err != nil {
						return err
					}

					href = resolved
					id := fmt.Sprintf("toc-%d", len(out)+1)
					out = append(out, item.Navigation{ID: id, ParentID: parent, Title: title, Href: href})
					next = id
				}
			}
		}
		// EPUB fixtures and simple nav documents may omit list elements.
		if n.Type == html.ElementNode && n.Data == "a" && parent == "" {
			href := attribute(n, "href")
			if href != "" {
				resolved, err := resolveEPUB(base, href)

				if err != nil {
					return err
				}

				href = resolved
				out = append(out, item.Navigation{ID: fmt.Sprintf("toc-%d", len(out)+1), Title: strings.TrimSpace(text(n)), Href: href})
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if e := walk(c, next, depth+1); e != nil {
				return e
			}
		}
		return nil
	}
	err := walk(doc, "", 0)
	return out, err
}

func resolveEPUB(base, reference string) (string, error) {
	u, err := url.Parse(reference)
	if err != nil || u.IsAbs() || u.Host != "" {
		return "", errors.New("EPUB resource must be a package-relative URI")
	}
	name := path.Join(base, u.Path)
	if strings.HasPrefix(name, "../") || strings.HasPrefix(u.Path, "/") {
		return "", errors.New("EPUB resource escapes package")
	}
	if u.Fragment != "" {
		name += "#" + u.Fragment
	}
	return name, nil
}
