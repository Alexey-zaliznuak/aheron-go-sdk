package docs

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

var (
	keyPattern    = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*(/[a-zA-Z0-9][a-zA-Z0-9._-]*)*$`)
	localePattern = regexp.MustCompile(`^[a-z]{2,3}(-[A-Za-z0-9]{2,8})*$`)
	shaPattern    = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
	digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

func ValidKey(s string) bool            { return len(s) <= 160 && keyPattern.MatchString(s) }
func ValidLocale(s string) bool         { return len(s) <= 32 && localePattern.MatchString(s) }
func ValidDigest(s string) bool         { return digestPattern.MatchString(s) }
func ValidSourceRevision(s string) bool { return shaPattern.MatchString(s) }

// CanonicalPackage returns a deep copy. V1 uses encoding/json's compact encoding
// of these ordered structs (including HTML escaping), UTF-8, LF line endings,
// sorted documents, relatedTopics and requires, and [] for empty collections.
// No other whitespace normalization is allowed: code indentation is significant.
func CanonicalPackage(in Package) (Package, []byte, error) {
	if in.FormatVersion != FormatVersion || !ValidKey(in.ProviderKey) || !ValidKey(in.ContractRevision) || !ValidSourceRevision(in.SourceRevision) {
		return Package{}, nil, errors.New("documentation: invalid package identity or format version")
	}
	if len(in.Documents) == 0 || len(in.Documents) > MaxDocuments {
		return Package{}, nil, errors.New("documentation: package must contain 1..64 documents")
	}
	out := in
	out.Documents = slices.Clone(in.Documents)
	for i := range out.Documents {
		d := &out.Documents[i]
		if !ValidKey(d.DocumentKey) || !ValidKey(d.TopicKey) || !ValidLocale(d.Locale) {
			return Package{}, nil, fmt.Errorf("documentation: invalid document identity at index %d", i)
		}
		texts := []*string{&d.Title, &d.Summary, &d.CommonMarkdown, &d.HumanAppendixMarkdown, &d.AgentAppendixMarkdown}
		for _, s := range texts {
			if !utf8.ValidString(*s) || strings.ContainsRune(*s, '\x00') {
				return Package{}, nil, errors.New("documentation: text must be valid UTF-8 without NUL")
			}
			*s = strings.ReplaceAll(strings.ReplaceAll(*s, "\r\n", "\n"), "\r", "\n")
		}
		if strings.TrimSpace(d.Title) == "" || len(d.Title) > 512 || strings.TrimSpace(d.Summary) == "" || len(d.Summary) > 2048 || strings.TrimSpace(d.CommonMarkdown) == "" {
			return Package{}, nil, fmt.Errorf("documentation: document %s needs title, summary and commonMarkdown within limits", d.DocumentKey)
		}
		d.RelatedTopics = append([]string{}, d.RelatedTopics...)
		slices.Sort(d.RelatedTopics)
		if len(d.RelatedTopics) > 32 {
			return Package{}, nil, errors.New("documentation: too many related topics")
		}
		for j, topic := range d.RelatedTopics {
			if !ValidKey(topic) || (j > 0 && topic == d.RelatedTopics[j-1]) {
				return Package{}, nil, errors.New("documentation: invalid or duplicate related topic")
			}
		}
		d.Requires = append([]Contract{}, d.Requires...)
		slices.SortFunc(d.Requires, func(a, b Contract) int { return strings.Compare(a.ProviderKey, b.ProviderKey) })
		if len(d.Requires) > 16 {
			return Package{}, nil, errors.New("documentation: too many contract dependencies")
		}
		for j, dep := range d.Requires {
			if !ValidKey(dep.ProviderKey) || !ValidKey(dep.ContractRevision) || (j > 0 && dep.ProviderKey == d.Requires[j-1].ProviderKey) {
				return Package{}, nil, errors.New("documentation: invalid or duplicate required provider")
			}
		}
	}
	slices.SortFunc(out.Documents, func(a, b Document) int {
		if n := strings.Compare(a.DocumentKey, b.DocumentKey); n != 0 {
			return n
		}
		return strings.Compare(a.Locale, b.Locale)
	})
	for i := 1; i < len(out.Documents); i++ {
		if out.Documents[i].DocumentKey == out.Documents[i-1].DocumentKey && out.Documents[i].Locale == out.Documents[i-1].Locale {
			return Package{}, nil, errors.New("documentation: duplicate document key and locale")
		}
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return Package{}, nil, err
	}
	if len(raw) > MaxPackageBytes {
		return Package{}, nil, errors.New("documentation: package exceeds 2 MiB")
	}
	return out, raw, nil
}

func SHA256(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

// DocumentSHA256 expects a document from CanonicalPackage.
func DocumentSHA256(d Document) string { raw, _ := json.Marshal(d); return SHA256(raw) }

func Render(d Document, audience string) (string, error) {
	var appendix string
	switch audience {
	case "human":
		appendix = d.HumanAppendixMarkdown
	case "agent":
		appendix = d.AgentAppendixMarkdown
	default:
		return "", errors.New("documentation: audience must be human or agent")
	}
	if appendix == "" {
		return d.CommonMarkdown, nil
	}
	return d.CommonMarkdown + "\n\n" + appendix, nil
}
