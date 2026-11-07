package trigger

// prListPagePoints is what GitHub charges the graphql pool for one page of forgeapi's PRList document, as
// testdata/prlist-cost.json records it (TestProbePRListCost regenerates it). GitHub prices a document from the page
// sizes it asks for, not the rows it returns, so a page costs this on any repository
// (https://docs.github.com/en/graphql/overview/rate-limits-and-query-limits-for-the-graphql-api).
const prListPagePoints = 2
