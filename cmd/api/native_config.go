package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
)

var nativeNamespace = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
var nativeHost = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,252}$`)

func milvusAddress(address string) bool {
	host, port, err := net.SplitHostPort(address)
	if err != nil || !nativeHost.MatchString(host) {
		return false
	}
	number, err := strconv.Atoi(port)
	return err == nil && number >= 1 && number <= 65535
}

func (s nativeSettings) validate() error {
	if s.SchemaVersion != "sea.search.native-milvus.v1" ||
		(s.Engine != "milvus" && s.Engine != "lite") ||
		!nativeNamespace.MatchString(s.Namespace) ||
		!s.Dense.valid() || !s.MultiVector.valid() {
		return errors.New("native backend requires a version, engine, namespace and two bounded HNSW configurations")
	}
	return nil
}

func (s hnswSettings) valid() bool {
	return s.M >= 2 && s.M <= 2048 && s.EFConstruction >= s.M &&
		s.EFConstruction <= 65536 && s.EFSearch >= 1 && s.EFSearch <= 65536
}

// The physical config changes collection identity. A duplicate or escaped
// alias cannot silently select different settings after a build is published.
func readNativeSettings(path string, out *nativeSettings) error {
	if path == "" || out == nil {
		return errors.New("native backend file required")
	}
	f, err := os.Open(path)
	if err != nil {
		return errors.New("native backend file unavailable")
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 32<<10+1))
	if err != nil || len(raw) > 32<<10 ||
		!literalNativeObject(raw, map[string]byte{
			"schema_version": '"', "engine": '"', "namespace": '"',
			"dense": '{', "multivector": '{'}, true) {
		return errors.New("native backend file requires five unique literal fields")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(out) != nil || decoder.Decode(new(any)) != io.EOF {
		return errors.New("native backend file is not one strict JSON object")
	}
	return nil
}

func literalNativeObject(raw []byte, fields map[string]byte, nested bool) bool {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return false
	}
	seen := make(map[string]bool, len(fields))
	for decoder.More() {
		start := decoder.InputOffset()
		keyToken, err := decoder.Token()
		key, ok := keyToken.(string)
		kind, allowed := fields[key]
		if err != nil || !ok || !allowed || seen[key] ||
			!literalJSONKeySpelling(raw, start, decoder.InputOffset(), key) {
			return false
		}
		seen[key] = true
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return false
		}
		value = bytes.TrimSpace(value)
		if len(value) == 0 || (kind == 'n' && (value[0] < '0' || value[0] > '9')) ||
			(kind != 'n' && value[0] != kind) {
			return false
		}
		if nested && kind == '{' && !literalNativeObject(value, map[string]byte{
			"m": 'n', "ef_construction": 'n', "ef_search": 'n'}, false) {
			return false
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') || len(seen) != len(fields) {
		return false
	}
	_, err = decoder.Token()
	return err == io.EOF && !strings.ContainsRune(string(raw), '\x00')
}
