package adblockgoparser

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

var (
	// ErrSkipComment Commented rules are skipped
	ErrSkipComment = errors.New("Commented rules are skipped")
	// ErrSkipHTML HTML rules are skipped
	ErrSkipHTML = errors.New("HTML rules are skipped")
	// ErrEmptyLine Empty lines are skipped
	ErrEmptyLine = errors.New("Empty lines are skipped")
	// ErrUnsupportedRule Unsupported option rules are skipped
	ErrUnsupportedRule = errors.New("Unsupported option rules are skipped")

	// Except domain
	supportedOptions = []string{
		"image",
		"script",
		"stylesheet",
		"font",
		"third-party",
		"xmlhttprequest",
		"match-case",
	}
	supportedOptionsPat = func() map[string]struct{} {
		rv := map[string]struct{}{}
		for _, key := range supportedOptions {
			rv[key] = struct{}{}
		}
		return rv
	}()
)

// Request has the expected data to be able to match the rules
type Request struct {
	// parsed full URL of the request
	URL *url.URL
	// a value of Origin header
	Origin string
	// a value of Referer header
	Referer string
	// Defines is request looks like XHLHttpRequest
	IsXHR bool
}

// RuleType type to identify the type of rule after parsing it
type RuleType int

const (
	AddressPart RuleType = iota
	DomainName
	ExactAddress
	RegexRule
)

// RuleAdBlock object containing the rule string generated Regex and parsed Options
type RuleAdBlock struct {
	RuleText    string
	Regex       *regexp.Regexp
	Options     map[string]bool
	IsException bool
	Domains     map[string]bool
	RuleType    RuleType
}

// ParseRule parse and create a RuleAdBlock from the string
func ParseRule(ruleText string) (*RuleAdBlock, error) {
	ruleText = strings.TrimSpace(ruleText)

	if ruleText == "" {
		return nil, ErrEmptyLine
	}

	if strings.HasPrefix(ruleText, "!") || strings.HasPrefix(ruleText, "[Adblock") {
		return nil, ErrSkipComment
	}

	if strings.Contains(ruleText, "##") || strings.Contains(ruleText, "#@#") || strings.Contains(ruleText, "#?#") {
		return nil, ErrSkipHTML
	}

	rule := &RuleAdBlock{
		RuleText: ruleText,
		Domains:  map[string]bool{},
		Options:  map[string]bool{},
	}

	rule.IsException = strings.HasPrefix(rule.RuleText, "@@")
	if rule.IsException {
		rule.RuleText = rule.RuleText[2:]
	}

	if strings.Contains(rule.RuleText, "$") {
		parts := strings.SplitN(rule.RuleText, "$", 2)
		rule.RuleText = parts[0]

		for _, option := range strings.Split(parts[1], ",") {
			optionNegative := !strings.HasPrefix(option, "~")
			option = strings.TrimPrefix(option, "~")
			_, supportedOption := supportedOptionsPat[option]

			switch {
			case strings.HasPrefix(option, "domain="):
				for _, domain := range strings.Split(option[len("domain="):], "|") {
					name := strings.TrimSpace(domain)
					rule.Domains[strings.TrimPrefix(name, "~")] = !strings.HasPrefix(name, "~")
				}
			case !supportedOption:
				return nil, ErrUnsupportedRule
			default:
				rule.Options[option] = optionNegative
			}
		}
	}

	rule.RuleType = AddressPart
	if strings.HasPrefix(rule.RuleText, "||") && strings.HasSuffix(rule.RuleText, "^") {
		rule.RuleType = DomainName
	}

	if strings.HasPrefix(rule.RuleText, "|") && strings.HasSuffix(rule.RuleText, "|") {
		rule.RuleType = ExactAddress
	}

	// The empty rule means the will block everything
	// /{anything}/ mean regular expression. or define some other pattern to conflict to a path like /anything/
	if rule.RuleText == "" || (strings.HasPrefix(rule.RuleText, "/") && strings.HasSuffix(rule.RuleText, "/")) {
		rule.RuleType = RegexRule
	}

	re, err := regexp.Compile(ruleToRegexp(rule))
	if err != nil {
		return nil, fmt.Errorf("Cannot compile Regex: %w", err)
	}
	rule.Regex = re
	return rule, nil
}

// RuleSet handle the structure to match whitelist and blacklist
type RuleSet struct {
	white *matcher
	black *matcher
}

// AddRule Adds rule in the correct matcher
func (ruleSet *RuleSet) AddRule(rule *RuleAdBlock) {
	if !rule.IsException {
		ruleSet.black.Add(rule)
	}
	if rule.IsException {
		ruleSet.white.Add(rule)
	}
}

// Allow return of the current request is allowed to proceed or should be avoided
func (ruleSet *RuleSet) Allow(req *Request) bool {
	return ruleSet.white.Match(req) || !ruleSet.black.Match(req)
}

// CreateRuleSet Creates a fresh new empty RuleSet
func CreateRuleSet() *RuleSet {
	return &RuleSet{
		white: &matcher{
			addressPartMatcher: &pathMatcher{
				next: map[rune]*pathMatcher{},
			},
			domainNameMatcher: &pathMatcher{
				next: map[rune]*pathMatcher{},
			},
			exactAddressMatcher: &pathMatcher{
				next: map[rune]*pathMatcher{},
			},
		},
		black: &matcher{
			addressPartMatcher: &pathMatcher{
				next: map[rune]*pathMatcher{},
			},
			domainNameMatcher: &pathMatcher{
				next: map[rune]*pathMatcher{},
			},
			exactAddressMatcher: &pathMatcher{
				next: map[rune]*pathMatcher{},
			},
		},
	}
}

func ruleToRegexp(r *RuleAdBlock) string {
	text := r.RuleText
	// Convert AdBlock rule to a regular expression.
	if text == "" {
		return ".*"
	}

	// Check if the rule isn't already regexp
	length := len(text)
	if length >= 2 && text[:1] == "/" && text[length-1:] == "/" {
		return text[1 : length-1]
	}

	// escape special Regex characters
	rule := text
	rule = regexp.QuoteMeta(rule)

	// |, ^ and * should not be escaped
	rule = strings.ReplaceAll(rule, `\|`, `|`)
	rule = strings.ReplaceAll(rule, `\^`, `^`)
	rule = strings.ReplaceAll(rule, `\*`, `*`)

	// XXX: the resulting Regex must use non-capturing groups (?:
	// for performance reasons; also, there is a limit on number
	// of capturing groups, no using them would prevent building
	// a single Regex out of several rules.

	// Separator character ^ matches anything but a letter, a digit, or
	// one of the following: _ - . %. The end of the address is also
	// accepted as separator.
	rule = strings.ReplaceAll(rule, "^", `(?:[^\w\d_\\\-.%]|$)`)

	// * symbol
	rule = strings.ReplaceAll(rule, "*", ".*")

	// | in the end means the end of the address
	length = len(rule)
	if rule[length-1] == '|' {
		rule = rule[:length-1] + "$"
	}

	// || in the beginning means beginning of the domain name
	if rule[:2] == "||" {
		// XXX: it is better to use urlparse for such things,
		// but urlparse doesn't give us a single Regex.
		// Regex is based on http://tools.ietf.org/html/rfc3986#appendix-B
		if len(rule) > 2 {
			//       |            | complete part       |
			//       |  scheme    | of the domain       |
			rule = `^(?:[^:/?#]+:)?(?://(?:[^/?#]*\.)?)?` + rule[2:]
		}
	} else if rule[0] == '|' {
		// | in the beginning means start of the address
		rule = "^" + rule[1:]
	}

	// If rule is case insensitive, use it on Regex
	if _, ok := r.Options["match-case"]; !ok {
		rule = "(?i)" + rule
	}

	// other | symbols should be escaped
	// we have "|$" in our regexp - do not touch it
	rule = regexp.MustCompile(`(\|)[^$]`).ReplaceAllString(rule, `\|`)
	return rule
}
