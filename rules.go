package adblockgoparser

import (
	"bufio"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net/url"
	"os"
	"path"
	"regexp"
	"strings"

	"github.com/google/logger"
)

var (
	ErrSkipComment     = errors.New("Commented rules are skipped")
	ErrSkipHTML        = errors.New("HTML rules are skipped")
	ErrEmptyLine       = errors.New("Empty lines are skipped")
	ErrUnsupportedRule = errors.New("Unsupported option rules are skipped")
	binaryOptions      = []string{
		"document",
		"domain",
		"elemhide",
		"font",
		"genericblock",
		"generichide",
		"image",
		"matchcase",
		"media",
		"object",
		"other",
		"ping",
		"popup",
		"script",
		"stylesheet",
		"subdocument",
		"thirdparty",
		"webrtc",
		"websocket",
		"xmlhttprequest",
	}
	optionsSplitPat = fmt.Sprintf(",(~?(?:%v))", strings.Join(binaryOptions, "|"))
	optionsSplitRe  = regexp.MustCompile(optionsSplitPat)
	// Except domain
	supportedOptions = []string{
		"image",
		"script",
		"stylesheet",
		"font",
		"thirdparty",
		"xmlhttprequest",
	}
	supportedOptionsPat = strings.Join(supportedOptions, ",")
)

// Structs

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

type ruleAdBlock struct {
	raw         string
	ruleText    string
	regexString string
	regex       *regexp.Regexp
	options     map[string]bool
	isException bool
	domains     map[string]bool
}

func parseRule(ruleText string) (*ruleAdBlock, error) {
	if strings.TrimSpace(ruleText) == "" {
		return nil, ErrEmptyLine
	}
	rule := &ruleAdBlock{
		domains:  map[string]bool{},
		options:  map[string]bool{},
		raw:      ruleText,
		ruleText: strings.TrimSpace(ruleText),
	}

	isComment := strings.Contains(rule.ruleText, "!") || strings.Contains(rule.ruleText, "[Adblock")
	if isComment {
		return nil, ErrSkipComment
	}

	isHTMLRule := strings.Contains(rule.ruleText, "##") || strings.Contains(rule.ruleText, "#@#")
	if isHTMLRule {
		return nil, ErrSkipHTML
	}

	rule.isException = strings.HasPrefix(rule.ruleText, "@@")
	if rule.isException {
		rule.ruleText = rule.ruleText[2:]
	}

	if strings.Contains(rule.ruleText, "$") {
		parts := strings.SplitN(rule.ruleText, "$", 2)
		length := len(parts)

		if length > 0 {
			rule.ruleText = parts[0]
		}

		if length > 1 {
			for _, option := range strings.Split(parts[1], ",") {
				if strings.HasPrefix(option, "domain=") {
					rule.domains = parseDomainOption(option)
				} else {
					optionName := strings.TrimPrefix(option, "~")
					if ok := strings.Contains(supportedOptionsPat, optionName); !ok {
						return nil, ErrUnsupportedRule
					}
					rule.options[optionName] = !strings.HasPrefix(option, "~")
				}
			}
		}
	}

	rule.regexString = ruleToRegexp(rule.ruleText)

	return rule, nil
}

type RuleSet struct {
	whitelist      []*ruleAdBlock
	whitelistRegex *regexp.Regexp
	blacklist      []*ruleAdBlock
	blacklistRegex *regexp.Regexp

	whitelistIncludeDomains      map[string][]*ruleAdBlock
	whitelistIncludeDomainsRegex map[string]*regexp.Regexp
	whitelistExcludeDomains      map[string][]*ruleAdBlock
	whitelistExcludeDomainsRegex map[string]*regexp.Regexp
	blacklistIncludeDomains      map[string][]*ruleAdBlock
	blacklistIncludeDomainsRegex map[string]*regexp.Regexp
	blacklistExcludeDomains      map[string][]*ruleAdBlock
	blacklistExcludeDomainsRegex map[string]*regexp.Regexp

	whitelistIncludeOptions      map[string][]*ruleAdBlock
	whitelistIncludeOptionsRegex map[string]*regexp.Regexp
	whitelistExcludeOptions      map[string][]*ruleAdBlock
	whitelistExcludeOptionsRegex map[string]*regexp.Regexp
	blacklistIncludeOptions      map[string][]*ruleAdBlock
	blacklistIncludeOptionsRegex map[string]*regexp.Regexp
	blacklistExcludeOptions      map[string][]*ruleAdBlock
	blacklistExcludeOptionsRegex map[string]*regexp.Regexp
}

func matchWhite(ruleSet RuleSet, req Request) bool {
	if ruleSet.whitelistRegex != nil && ruleSet.whitelistRegex.MatchString(req.URL.String()) {
		return true
	}

	options := extractOptionsFromRequest(req)
	for option, active := range options {
		if ruleSet.whitelistIncludeOptionsRegex[option] != nil && ruleSet.whitelistIncludeOptionsRegex[option].MatchString(req.URL.String()) {
			return active == true
		}
		if ruleSet.whitelistExcludeOptionsRegex[option] != nil && ruleSet.whitelistExcludeOptionsRegex[option].MatchString(req.URL.String()) {
			return active == false
		}
	}
	return false
}

func matchBlack(ruleSet RuleSet, req Request) bool {
	if ruleSet.blacklistRegex != nil && ruleSet.blacklistRegex.MatchString(req.URL.String()) {
		return true
	}

	disabledForDomain := false
	for domain := range ruleSet.blacklistExcludeDomainsRegex {
		disabledForDomain = strings.Contains(req.URL.Hostname(), domain)
	}
	didMatch := false
	if !disabledForDomain {
		for domain := range ruleSet.blacklistIncludeDomainsRegex {
			lookForDomain := strings.Contains(req.URL.Hostname(), domain)
			if lookForDomain &&
				ruleSet.blacklistIncludeDomainsRegex[domain] != nil &&
				ruleSet.blacklistIncludeDomainsRegex[domain].MatchString(req.URL.String()) {
				fmt.Println("blacklistIncludeDomainsRegex", domain, "lookForDomain", lookForDomain, "didMatch", didMatch)
				return true
			}
		}
	}

	options := extractOptionsFromRequest(req)
	for option, active := range options {
		if ruleSet.blacklistIncludeOptionsRegex[option] != nil && ruleSet.blacklistIncludeOptionsRegex[option].MatchString(req.URL.String()) {
			return active == true
		}
		if ruleSet.blacklistExcludeOptionsRegex[option] != nil && ruleSet.blacklistExcludeOptionsRegex[option].MatchString(req.URL.String()) {
			return active == false
		}
	}
	return false
}

func (ruleSet *RuleSet) Allow(req Request) bool {
	if ok := matchWhite(*ruleSet, req); ok {
		return true
	}
	if ok := matchBlack(*ruleSet, req); ok {
		return false
	}
	return true
}

func readLines(path string) ([]string, error) {
	f, err := os.Open(path)

	if err != nil {
		return nil, err
	}
	defer f.Close()

	reader := bufio.NewReader(f)
	lines := []string{}
	for line := []byte{}; err == nil; line, _, err = reader.ReadLine() {
		sl := strings.TrimSuffix(string(line), "\n\r")
		if len(sl) == 0 {
			continue
		}
		lines = append(lines, sl)
	}

	return lines, nil
}

func NewRulesSetFromFile(path string) (*RuleSet, error) {
	lines, err := readLines(path)
	if err != nil {
		return nil, err
	}
	return NewRuleSetFromStr(lines)
}

func NewRuleSetFromStr(rulesStr []string) (*RuleSet, error) {
	logger.Init("NewRuleSetFromStr", true, true, ioutil.Discard)
	logger.SetFlags(log.LstdFlags)

	ruleSet := &RuleSet{
		whitelistIncludeDomains:      map[string][]*ruleAdBlock{},
		whitelistIncludeDomainsRegex: map[string]*regexp.Regexp{},
		whitelistExcludeDomains:      map[string][]*ruleAdBlock{},
		whitelistExcludeDomainsRegex: map[string]*regexp.Regexp{},
		blacklistIncludeDomains:      map[string][]*ruleAdBlock{},
		blacklistIncludeDomainsRegex: map[string]*regexp.Regexp{},
		blacklistExcludeDomains:      map[string][]*ruleAdBlock{},
		blacklistExcludeDomainsRegex: map[string]*regexp.Regexp{},

		whitelistIncludeOptions:      map[string][]*ruleAdBlock{},
		whitelistIncludeOptionsRegex: map[string]*regexp.Regexp{},
		whitelistExcludeOptions:      map[string][]*ruleAdBlock{},
		whitelistExcludeOptionsRegex: map[string]*regexp.Regexp{},
		blacklistIncludeOptions:      map[string][]*ruleAdBlock{},
		blacklistIncludeOptionsRegex: map[string]*regexp.Regexp{},
		blacklistExcludeOptions:      map[string][]*ruleAdBlock{},
		blacklistExcludeOptionsRegex: map[string]*regexp.Regexp{},
	}

	// Start parsing
	for _, ruleStr := range rulesStr {
		rule, err := parseRule(ruleStr)
		switch {
		case err == nil:
			if len(rule.domains) > 0 && len(rule.options) == 0 {
				for domain, allowed := range rule.domains {
					if allowed {
						ruleSet.blacklistIncludeDomains[domain] = append(ruleSet.blacklistIncludeDomains[domain], rule)
					} else {
						ruleSet.blacklistExcludeDomains[domain] = append(ruleSet.blacklistExcludeDomains[domain], rule)
					}
				}
				continue
			}
			if len(rule.options) > 0 {
				if rule.isException {
					for option, allowed := range rule.options {
						if allowed {
							ruleSet.whitelistIncludeOptions[option] = append(ruleSet.whitelistIncludeOptions[option], rule)
						} else {
							ruleSet.whitelistExcludeOptions[option] = append(ruleSet.whitelistExcludeOptions[option], rule)
						}
					}
				} else {
					for option, allowed := range rule.options {
						if allowed {
							ruleSet.blacklistIncludeOptions[option] = append(ruleSet.blacklistIncludeOptions[option], rule)
						} else {
							ruleSet.blacklistExcludeOptions[option] = append(ruleSet.blacklistExcludeOptions[option], rule)
						}
					}
				}
			} else {
				if rule.isException {
					ruleSet.whitelist = append(ruleSet.whitelist, rule)
				} else {
					ruleSet.blacklist = append(ruleSet.blacklist, rule)
				}
			}
		case errors.Is(err, ErrSkipComment),
			errors.Is(err, ErrSkipHTML),
			errors.Is(err, ErrUnsupportedRule):
			logger.Info(err, ": ", strings.TrimSpace(ruleStr))
		case errors.Is(err, ErrEmptyLine):
			logger.Info(err)
		default:
			logger.Info("cannot parse rule: ", err)
			return nil, fmt.Errorf("cannot parse rule: %w", err)
		}
	}

	compileAllRegex(ruleSet)
	return ruleSet, nil
}

func CombinedRegex(rules []*ruleAdBlock) *regexp.Regexp {
	regexes := []string{}
	for _, rule := range rules {
		regexes = append(regexes, rule.regexString)
	}
	rs := strings.Join(regexes, "|")
	if rs == "" {
		return nil
	}
	return regexp.MustCompile(rs)
}

func compileAllRegex(ruleSet *RuleSet) {
	ruleSet.whitelistRegex = CombinedRegex(ruleSet.whitelist)
	ruleSet.blacklistRegex = CombinedRegex(ruleSet.blacklist)
	for option, _ := range ruleSet.whitelistIncludeOptions {
		ruleSet.whitelistIncludeOptionsRegex[option] = CombinedRegex(ruleSet.whitelistIncludeOptions[option])
	}
	for option, _ := range ruleSet.whitelistExcludeOptions {
		ruleSet.whitelistExcludeOptionsRegex[option] = CombinedRegex(ruleSet.whitelistExcludeOptions[option])
	}
	for option, _ := range ruleSet.blacklistIncludeOptions {
		ruleSet.blacklistIncludeOptionsRegex[option] = CombinedRegex(ruleSet.blacklistIncludeOptions[option])
	}
	for option, _ := range ruleSet.blacklistExcludeOptions {
		ruleSet.blacklistExcludeOptionsRegex[option] = CombinedRegex(ruleSet.blacklistExcludeOptions[option])
	}
	for option, _ := range ruleSet.whitelistIncludeDomains {
		ruleSet.whitelistIncludeDomainsRegex[option] = CombinedRegex(ruleSet.whitelistIncludeDomains[option])
	}
	for option, _ := range ruleSet.whitelistExcludeDomains {
		ruleSet.whitelistExcludeDomainsRegex[option] = CombinedRegex(ruleSet.whitelistExcludeDomains[option])
	}
	for option, _ := range ruleSet.blacklistIncludeDomains {
		ruleSet.blacklistIncludeDomainsRegex[option] = CombinedRegex(ruleSet.blacklistIncludeDomains[option])
	}
	for option, _ := range ruleSet.blacklistExcludeDomains {
		ruleSet.blacklistExcludeDomainsRegex[option] = CombinedRegex(ruleSet.blacklistExcludeDomains[option])
	}
}

func parseDomainOption(text string) map[string]bool {
	domains := text[len("domain="):]
	parts := strings.Split(domains, "|")
	opts := make(map[string]bool, len(parts))

	for _, part := range parts {
		opts[strings.TrimPrefix(part, "~")] = !strings.HasPrefix(part, "~")
	}

	return opts
}

func ruleToRegexp(text string) string {
	// Convert AdBlock rule to a regular expression.
	if text == "" {
		return ".*"
	}

	// Check if the rule isn't already regexp
	length := len(text)
	if length >= 2 && text[:1] == "/" && text[length-1:] == "/" {
		return text[1 : length-1]
	}

	// escape special regex characters
	rule := text
	rule = regexp.QuoteMeta(rule)

	// |, ^ and * should not be escaped
	rule = strings.ReplaceAll(rule, `\|`, `|`)
	rule = strings.ReplaceAll(rule, `\^`, `^`)
	rule = strings.ReplaceAll(rule, `\*`, `*`)

	// XXX: the resulting regex must use non-capturing groups (?:
	// for performance reasons; also, there is a limit on number
	// of capturing groups, no using them would prevent building
	// a single regex out of several rules.

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
		// but urlparse doesn't give us a single regex.
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

	// other | symbols should be escaped
	// we have "|$" in our regexp - do not touch it
	rule = regexp.MustCompile(`(\|)[^$]`).ReplaceAllString(rule, `\|`)
	return rule
}

func extractOptionsFromRequest(req Request) map[string]bool {
	result := make(map[string]bool, len(supportedOptions))

	filename := path.Base(req.URL.Path)
	result["script"] = regexp.MustCompile(`(?:\.js$|\.js\.gz$)`).MatchString(filename)
	result["image"] = regexp.MustCompile(`(?:\.jpg$|\.jpeg$|\.png$|\.gif$|\.webp$|\.tiff$|\.psd$|\.raw$|\.bmp$|\.heif$|\.indd$|\.jpeg2000$)`).MatchString(filename)
	result["stylesheet"] = regexp.MustCompile(`(?:\.css$)`).MatchString(filename)
	// More font extension at https://fileinfo.com/filetypes/font
	result["font"] = regexp.MustCompile(`(?:\.otf|\.ttf|\.fnt)`).MatchString(filename)
	result["thirdparty"] = req.Referer != ""
	result["xmlhttprequest"] = req.IsXHR

	return result
}
