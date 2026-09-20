package types

import searchclient "github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/searchclient"

type (
	SearchRequest                   = searchclient.ContentSearchRequest
	SearchResponse                  = searchclient.ContentSearchResponse
	ContentSearchRequest            = searchclient.ContentSearchRequest
	ContentSearchResponse           = searchclient.ContentSearchResponse
	StructuredSearchRequest         = searchclient.StructuredSearchRequest
	TitleSearchResponse             = searchclient.ArticleTitleSearchResponse
	AuthorSearchResponse            = searchclient.AuthorNameSearchResponse
	OnboardingQuestionnaireRequest  = searchclient.OnboardingQuestionnaireRequest
	OnboardingQuestionnaireResponse = searchclient.OnboardingQuestionnaireResponse
)

var (
	ErrSourceMetadataUnavailable   = searchclient.ErrSourceMetadataUnavailable
	ErrOnboardingMemoryUnavailable = searchclient.ErrOnboardingMemoryUnavailable
	ErrInvalidOnboardingAnswer     = searchclient.ErrInvalidOnboardingAnswer
)
