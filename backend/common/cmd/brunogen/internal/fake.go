package gen

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
)

// scenarioSeqMu guards scenarioSeqMap.
var scenarioSeqMu sync.Mutex

// scenarioSeqMap tracks the per-scenario sequential counter for the nonce type.
// Keys are scenario basenames; values are the last-issued sequence number.
var scenarioSeqMap = map[string]int64{}

// nextScenarioSeq returns the next sequential counter for the named scenario.
// The counter starts at 1 and is scoped to the scenario basename so that each
// scenario has its own independent sequence.
func nextScenarioSeq(scenarioBasename string) int64 {
	scenarioSeqMu.Lock()
	defer scenarioSeqMu.Unlock()
	scenarioSeqMap[scenarioBasename]++
	return scenarioSeqMap[scenarioBasename]
}

// fakeTemplatePlaceholderRe matches {{typeName}} placeholders in $fake template
// strings. Only matches word-character type names (no $ prefix), so resolved
// Bruno variables like {{$randomUUID}} are left untouched.
var fakeTemplatePlaceholderRe = regexp.MustCompile(`\{\{(\w+)\}\}`)

// resolveFakeTemplate resolves a $fake template string by replacing each
// {{typeName}} placeholder with the resolved fake value for that type.
// If the template contains no {{}} placeholders, the whole string is treated
// as a single type name (scalar shorthand, e.g. $fake: uuid).
func resolveFakeTemplate(tmpl, scenarioBasename string) string {
	if !strings.Contains(tmpl, "{{") {
		return resolveFakeItem(tmpl, scenarioBasename)
	}
	return fakeTemplatePlaceholderRe.ReplaceAllStringFunc(tmpl, func(match string) string {
		sub := fakeTemplatePlaceholderRe.FindStringSubmatch(match)
		return resolveFakeItem(sub[1], scenarioBasename)
	})
}

// resolveFakeItem resolves a single $fake type name. The special "nonce" type
// produces a "{{$timestamp}}-{seq}" string: the Bruno runtime variable makes
// it unique across test runs, and the scenario-scoped counter makes it unique
// within a single run.
func resolveFakeItem(fakeType, scenarioBasename string) string {
	if strings.ToLower(fakeType) == "nonce" {
		return fmt.Sprintf("{{$timestamp}}-%d", nextScenarioSeq(scenarioBasename))
	}
	return resolveFakeString(fakeType)
}

// fakeEntry maps a $fake type name to its Bruno dynamic variable.
type fakeEntry struct {
	Bruno   string // Bruno dynamic variable, e.g. "{{$randomUUID}}"
	Numeric bool   // true when the value is a non-string JSON type
}

// fakeTypes is the canonical mapping from $fake type names to Bruno generators.
// All variables are from https://docs.usebruno.com/testing/script/dynamic-variables
var fakeTypes = map[string]fakeEntry{
	// Composite (composed from multiple Bruno variables)
	"sku": {"{{$randomProductName}} {{$randomBankAccount}}", false},

	// Basic Data Types
	"guid":         {"{{$guid}}", false},
	"timestamp":    {"{{$timestamp}}", true},
	"isotimestamp": {"{{$isoTimestamp}}", false},
	"uuid":         {"{{$randomUUID}}", false},
	"nanoid":       {"{{$randomNanoId}}", false},
	"alphanumeric": {"{{$randomAlphaNumeric}}", false},
	"bool":         {"{{$randomBoolean}}", true},
	"boolean":      {"{{$randomBoolean}}", true},
	"int":          {"{{$randomInt}}", true},
	"integer":      {"{{$randomInt}}", true},
	"color":        {"{{$randomColor}}", false},
	"hexcolor":     {"{{$randomHexColor}}", false},
	"abbreviation": {"{{$randomAbbreviation}}", false},
	"word":         {"{{$randomWord}}", false},
	"words":        {"{{$randomWords}}", false},

	// Internet & Network
	"domain":       {"{{$randomDomainName}}", false},
	"domainsuffix": {"{{$randomDomainSuffix}}", false},
	"domainword":   {"{{$randomDomainWord}}", false},
	"email":        {"{{$randomEmail}}", false},
	"exampleemail": {"{{$randomExampleEmail}}", false},
	"ip":           {"{{$randomIP}}", false},
	"ipv4":         {"{{$randomIPV4}}", false},
	"ipv6":         {"{{$randomIPV6}}", false},
	"locale":       {"{{$randomLocale}}", false},
	"mac":          {"{{$randomMACAddress}}", false},
	"password":     {"{{$randomPassword}}", false},
	"protocol":     {"{{$randomProtocol}}", false},
	"semver":       {"{{$randomSemver}}", false},
	"url":          {"{{$randomUrl}}", false},
	"useragent":    {"{{$randomUserAgent}}", false},
	"username":     {"{{$randomUserName}}", false},

	// Names & Personal Information
	"firstname":     {"{{$randomFirstName}}", false},
	"lastname":      {"{{$randomLastName}}", false},
	"fullname":      {"{{$randomFullName}}", false},
	"name":          {"{{$randomFullName}}", false},
	"nameprefix":    {"{{$randomNamePrefix}}", false},
	"namesuffix":    {"{{$randomNameSuffix}}", false},
	"jobarea":       {"{{$randomJobArea}}", false},
	"jobdescriptor": {"{{$randomJobDescriptor}}", false},
	"jobtitle":      {"{{$randomJobTitle}}", false},
	"jobtype":       {"{{$randomJobType}}", false},
	"phone":         {"{{$randomPhoneNumber}}", false},
	"phoneext":      {"{{$randomPhoneNumberExt}}", false},

	// Location
	"city":        {"{{$randomCity}}", false},
	"country":     {"{{$randomCountry}}", false},
	"countrycode": {"{{$randomCountryCode}}", false},
	"lat":         {"{{$randomLatitude}}", false},
	"lon":         {"{{$randomLongitude}}", false},
	"street":      {"{{$randomStreetAddress}}", false},
	"streetname":  {"{{$randomStreetName}}", false},

	// Images
	"avatar":         {"{{$randomAvatarImage}}", false},
	"image":          {"{{$randomImageUrl}}", false},
	"abstractimage":  {"{{$randomAbstractImage}}", false},
	"animalsimage":   {"{{$randomAnimalsImage}}", false},
	"businessimage":  {"{{$randomBusinessImage}}", false},
	"catsimage":      {"{{$randomCatsImage}}", false},
	"cityimage":      {"{{$randomCityImage}}", false},
	"foodimage":      {"{{$randomFoodImage}}", false},
	"nightlifeimage": {"{{$randomNightlifeImage}}", false},
	"fashionimage":   {"{{$randomFashionImage}}", false},
	"peopleimage":    {"{{$randomPeopleImage}}", false},
	"natureimage":    {"{{$randomNatureImage}}", false},
	"sportsimage":    {"{{$randomSportsImage}}", false},
	"transportimage": {"{{$randomTransportImage}}", false},
	"imagedatauri":   {"{{$randomImageDataUri}}", false},

	// Finance
	"bankaccount":     {"{{$randomBankAccount}}", false},
	"bankaccountname": {"{{$randomBankAccountName}}", false},
	"bic":             {"{{$randomBankAccountBic}}", false},
	"bitcoin":         {"{{$randomBitcoin}}", false},
	"creditcard":      {"{{$randomCreditCardMask}}", false},
	"currency":        {"{{$randomCurrencyCode}}", false},
	"currencyname":    {"{{$randomCurrencyName}}", false},
	"currencysymbol":  {"{{$randomCurrencySymbol}}", false},
	"iban":            {"{{$randomBankAccountIban}}", false},
	"transactiontype": {"{{$randomTransactionType}}", false},

	// Business
	"bs":                    {"{{$randomBs}}", false},
	"bsadjective":           {"{{$randomBsAdjective}}", false},
	"bsbuzz":                {"{{$randomBsBuzz}}", false},
	"bsnoun":                {"{{$randomBsNoun}}", false},
	"catchphrase":           {"{{$randomCatchPhrase}}", false},
	"catchphraseadjective":  {"{{$randomCatchPhraseAdjective}}", false},
	"catchphrasedescriptor": {"{{$randomCatchPhraseDescriptor}}", false},
	"catchphrasenoun":       {"{{$randomCatchPhraseNoun}}", false},
	"company":               {"{{$randomCompanyName}}", false},
	"companysuffix":         {"{{$randomCompanySuffix}}", false},

	// Database
	"dbcollation": {"{{$randomDatabaseCollation}}", false},
	"dbcolumn":    {"{{$randomDatabaseColumn}}", false},
	"dbengine":    {"{{$randomDatabaseEngine}}", false},
	"dbtype":      {"{{$randomDatabaseType}}", false},

	// Dates
	"datefuture": {"{{$randomDateFuture}}", false},
	"datepast":   {"{{$randomDatePast}}", false},
	"daterecent": {"{{$randomDateRecent}}", false},
	"month":      {"{{$randomMonth}}", false},
	"weekday":    {"{{$randomWeekday}}", false},

	// Files & System
	"commonfileext":  {"{{$randomCommonFileExt}}", false},
	"commonfilename": {"{{$randomCommonFileName}}", false},
	"commonfiletype": {"{{$randomCommonFileType}}", false},
	"dirpath":        {"{{$randomDirectoryPath}}", false},
	"fileext":        {"{{$randomFileExt}}", false},
	"filename":       {"{{$randomFileName}}", false},
	"filepath":       {"{{$randomFilePath}}", false},
	"filetype":       {"{{$randomFileType}}", false},
	"mimetype":       {"{{$randomMimeType}}", false},

	// Commerce
	"department":       {"{{$randomDepartment}}", false},
	"price":            {"{{$randomPrice}}", false},
	"product":          {"{{$randomProduct}}", false},
	"productadjective": {"{{$randomProductAdjective}}", false},
	"productmaterial":  {"{{$randomProductMaterial}}", false},
	"productname":      {"{{$randomProductName}}", false},

	// Hacker & Lorem
	"adjective":      {"{{$randomAdjective}}", false},
	"ingverb":        {"{{$randomIngverb}}", false},
	"loremlines":     {"{{$randomLoremLines}}", false},
	"loremparas":     {"{{$randomLoremParagraphs}}", false},
	"loremsentences": {"{{$randomLoremSentences}}", false},
	"loremslug":      {"{{$randomLoremSlug}}", false},
	"loremtext":      {"{{$randomLoremText}}", false},
	"loremword":      {"{{$randomLoremWord}}", false},
	"loremwords":     {"{{$randomLoremWords}}", false},
	"noun":           {"{{$randomNoun}}", false},
	"paragraph":      {"{{$randomLoremParagraph}}", false},
	"phrase":         {"{{$randomPhrase}}", false},
	"sentence":       {"{{$randomLoremSentence}}", false},
	"verb":           {"{{$randomVerb}}", false},
}

func lookupFakeEntry(fakeType string) (fakeEntry, bool) {
	entry, ok := fakeTypes[strings.ToLower(fakeType)]
	return entry, ok
}

// resolveFakeString resolves a single $fake type name to its Bruno variable
// string, falling back to a camel-cased {{$random...}} variable for unknown types.
func resolveFakeString(fakeType string) string {
	if entry, ok := lookupFakeEntry(fakeType); ok {
		return entry.Bruno
	}
	return "{{$random" + strings.ToUpper(fakeType[:1]) + fakeType[1:] + "}}"
}
