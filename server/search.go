package server

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"github.com/btcsuite/btcutil/base58"
	"github.com/golang/protobuf/ptypes/wrappers"
	pb "github.com/lbryio/hub/protobuf/go"
	"math"

	//"github.com/lbryio/hub/schema"
	"github.com/lbryio/hub/util"
	"github.com/olivere/elastic/v7"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
	"gopkg.in/karalabe/cookiejar.v1/collections/deque"
	"log"
	"reflect"
	"sort"
	"strings"
)

type record struct {
	Txid string   	 `json:"tx_id"`
	Nout uint32   	 `json:"tx_nout"`
	Height uint32 	 `json:"height"`
	ClaimId string 	 `json:"claim_id"`
	ChannelId string `json:"channel_id"`
}

type compareFunc func(r1, r2 **record, invert bool) int

type multiSorter struct {
	records []*record
	compare []compareFunc
	invert  []bool
}

var compareFuncs = map[string]compareFunc {
	"height": func(r1, r2 **record, invert bool) int {
		var res = 0
		if (*r1).Height < (*r2).Height {
			res = -1
		} else if (*r1).Height > (*r2).Height {
			res = 1
		}
		if invert {
			res = res * -1
		}
		return res
	},
}

// Sort sorts the argument slice according to the less functions passed to OrderedBy.
func (ms *multiSorter) Sort(records []*record) {
	ms.records = records
	sort.Sort(ms)
}

// OrderedBy returns a Sorter that sorts using the less functions, in order.
// Call its Sort method to sort the data.
func OrderedBy(compare ...compareFunc) *multiSorter {
	return &multiSorter{
		compare: compare,
	}
}

// Len is part of sort.Interface.
func (ms *multiSorter) Len() int {
	return len(ms.records)
}

// Swap is part of sort.Interface.
func (ms *multiSorter) Swap(i, j int) {
	ms.records[i], ms.records[j] = ms.records[j], ms.records[i]
}

// Less is part of sort.Interface. It is implemented by looping along the
// less functions until it finds a comparison that discriminates between
// the two items (one is less than the other). Note that it can call the
// less functions twice per call. We could change the functions to return
// -1, 0, 1 and reduce the number of calls for greater efficiency: an
// exercise for the reader.
func (ms *multiSorter) Less(i, j int) bool {
	p, q := &ms.records[i], &ms.records[j]
	// Try all but the last comparison.
	var k int
	for k = 0; k < len(ms.compare)-1; k++ {
		cmp := ms.compare[k]
		res := cmp(p, q, ms.invert[k])

		if res != 0 {
			return res > 0
		}
	}
	// All comparisons to here said "equal", so just return whatever
	// the final comparison reports.
	return ms.compare[k](p, q, ms.invert[k]) > 0
}

type orderField struct {
	Field string
	IsAsc bool
}
const (
	errorResolution = iota
	channelResolution = iota
	streamResolution = iota
)
type urlResolution struct {
	resolutionType 	int
	value 			string
}

func StrArrToInterface(arr []string) []interface{} {
	searchVals := make([]interface{}, len(arr))
	for i := 0; i < len(arr); i++ {
		searchVals[i] = arr[i]
	}
	return searchVals
}

func AddTermsField(arr []string, name string, q *elastic.BoolQuery) *elastic.BoolQuery {
	if len(arr) > 0 {
		searchVals := StrArrToInterface(arr)
		return q.Must(elastic.NewTermsQuery(name, searchVals...))
	}
	return q
}

func AddIndividualTermFields(arr []string, name string, q *elastic.BoolQuery, invert bool) *elastic.BoolQuery {
	if len(arr) > 0 {
		for _, x := range arr {
			if invert {
				q = q.MustNot(elastic.NewTermQuery(name, x))
			} else {
				q = q.Must(elastic.NewTermQuery(name, x))
			}
		}
		return q
	}
	return q
}

func AddRangeField(rq *pb.RangeField, name string, q *elastic.BoolQuery) *elastic.BoolQuery {
	if rq == nil {
		return q
	}

	if len(rq.Value) > 1 {
		if rq.Op != pb.RangeField_EQ {
			return q
		}
		return AddTermsField(rq.Value, name, q)
	}
	if rq.Op == pb.RangeField_EQ {
		return q.Must(elastic.NewTermQuery(name, rq.Value[0]))
	} else if rq.Op == pb.RangeField_LT {
		return q.Must(elastic.NewRangeQuery(name).Lt(rq.Value[0]))
	} else if rq.Op == pb.RangeField_LTE {
		return q.Must(elastic.NewRangeQuery(name).Lte(rq.Value[0]))
	} else if rq.Op == pb.RangeField_GT {
		return q.Must(elastic.NewRangeQuery(name).Gt(rq.Value[0]))
	} else { // pb.RangeField_GTE
		return q.Must(elastic.NewRangeQuery(name).Gte(rq.Value[0]))
	}
}

func AddInvertibleField(field *pb.InvertibleField, name string, q *elastic.BoolQuery) *elastic.BoolQuery {
	if field == nil {
		return q
	}
	searchVals := StrArrToInterface(field.Value)
	if field.Invert {
		q = q.MustNot(elastic.NewTermsQuery(name, searchVals...))
		if name == "channel_id.keyword" {
			q = q.MustNot(elastic.NewTermsQuery("_id", searchVals...))
		}
		return q
	} else {
		return q.Must(elastic.NewTermsQuery(name, searchVals...))
	}
}

func (s *Server) normalizeTag(tag string) string {
	c := cases.Lower(language.English)
	res := s.MultiSpaceRe.ReplaceAll(
		s.WeirdCharsRe.ReplaceAll(
			[]byte(strings.TrimSpace(strings.Replace(c.String(tag), "'", "", -1))),
			[]byte(" ")),
		[]byte(" "))

	return string(res)
}


func (s *Server) cleanTags(tags []string) []string {
	cleanedTags := make([]string, len(tags))
	for i, tag := range tags {
		cleanedTags[i] = s.normalizeTag(tag)
	}
	return cleanedTags
}

func (s *Server) Search(ctx context.Context, in *pb.SearchRequest) (*pb.Outputs, error) {
	var client *elastic.Client = nil
	if s.EsClient == nil {
		esUrl := s.Args.EsHost + ":" + s.Args.EsPort
		tmpClient, err := elastic.NewClient(elastic.SetURL(esUrl), elastic.SetSniff(false))
		if err != nil {
			return nil, err
		}
		client = tmpClient
		s.EsClient = client
	} else {
		client = s.EsClient
	}

	claimTypes := map[string]int {
		"stream": 1,
		"channel": 2,
		"repost": 3,
		"collection": 4,
	}

	streamTypes := map[string]int {
		"video": 1,
		"audio": 2,
		"image": 3,
		"document": 4,
		"binary": 5,
		"model": 6,
	}

	replacements := map[string]string {
		"name": "normalized",
		"txid": "tx_id",
		"claim_hash": "_id",
	}

	textFields := map[string]bool {
		"author": true,
		"canonical_url": true,
		"channel_id": true,
		"claim_name": true,
		"description": true,
		"claim_id": true,
		"media_type": true,
		"normalized": true,
		"public_key_bytes": true,
		"public_key_hash": true,
		"short_url": true,
		"signature": true,
		"signature_digest": true,
		"stream_type": true,
		"title": true,
		"tx_id": true,
		"fee_currency": true,
		"reposted_claim_id": true,
		"tags": true,
	}

	var from = 0
	var size = 1000
	var pageSize = 10
	var orderBy []orderField
	var ms *multiSorter

	// Ping the Elasticsearch server to get e.g. the version number
	//_, code, err := client.Ping("http://127.0.0.1:9200").Do(ctx)
	//if err != nil {
	//	return nil, err
	//}
	//if code != 200 {
	//	return nil, errors.New("ping failed")
	//}

	// TODO: support all of this https://github.com/lbryio/lbry-sdk/blob/master/lbry/wallet/server/db/elasticsearch/search.py#L385

	q := elastic.NewBoolQuery()

	if in.IsControlling != nil {
		q = q.Must(elastic.NewTermQuery("is_controlling", in.IsControlling.Value))
	}

	if in.AmountOrder != nil {
		in.Limit.Value = 1
		in.OrderBy = []string{"effective_amount"}
		in.Offset = &wrappers.Int32Value{Value: in.AmountOrder.Value - 1}
	}

	if in.Limit != nil {
		pageSize = int(in.Limit.Value)
		log.Printf("page size: %d\n", pageSize)
	}

	if in.Offset != nil {
		from = int(in.Offset.Value)
	}

	if len(in.Name) > 0 {
		normalized := make([]string, len(in.Name))
		for i := 0; i < len(in.Name); i++ {
			normalized[i] = util.Normalize(in.Name[i])
		}
		in.Normalized = normalized
	}

	if len(in.OrderBy) > 0 {
		for _, x := range in.OrderBy {
			var toAppend string
			var isAsc = false
			if x[0] == '^' {
				isAsc = true
				x = x[1:]
			}
			if _, ok := replacements[x]; ok {
				toAppend = replacements[x]
			} else {
				toAppend = x
			}

			if _, ok := textFields[toAppend]; ok {
				toAppend = toAppend + ".keyword"
			}
			orderBy = append(orderBy, orderField{toAppend, isAsc})
		}

		ms = &multiSorter{
			invert: make([]bool, len(orderBy)),
			compare: make([]compareFunc, len(orderBy)),
		}
		for i, x := range orderBy {
			ms.compare[i] = compareFuncs[x.Field]
			ms.invert[i] = x.IsAsc
		}
	}

	if len(in.ClaimType) > 0 {
		searchVals := make([]interface{}, len(in.ClaimType))
		for i := 0; i < len(in.ClaimType); i++ {
			searchVals[i] = claimTypes[in.ClaimType[i]]
		}
		q = q.Must(elastic.NewTermsQuery("claim_type", searchVals...))
	}

	if len(in.StreamType) > 0 {
		searchVals := make([]interface{}, len(in.StreamType))
		for i := 0; i < len(in.StreamType); i++ {
			searchVals[i] = streamTypes[in.StreamType[i]]
		}
		q = q.Must(elastic.NewTermsQuery("stream_type", searchVals...))
	}

	if len(in.XId) > 0 {
		searchVals := make([]interface{}, len(in.XId))
		for i := 0; i < len(in.XId); i++ {
			util.ReverseBytes(in.XId[i])
			searchVals[i] = hex.Dump(in.XId[i])
		}
		if len(in.XId) == 1 && len(in.XId[0]) < 20 {
			q = q.Must(elastic.NewPrefixQuery("_id", string(in.XId[0])))
		} else {
			q = q.Must(elastic.NewTermsQuery("_id", searchVals...))
		}
	}


	if in.ClaimId != nil {
		searchVals := StrArrToInterface(in.ClaimId.Value)
		if len(in.ClaimId.Value) == 1 && len(in.ClaimId.Value[0]) < 20 {
			if in.ClaimId.Invert {
				q = q.MustNot(elastic.NewPrefixQuery("claim_id.keyword", in.ClaimId.Value[0]))
			} else {
				q = q.Must(elastic.NewPrefixQuery("claim_id.keyword", in.ClaimId.Value[0]))
			}
		} else {
			if in.ClaimId.Invert {
				q = q.MustNot(elastic.NewTermsQuery("claim_id.keyword", searchVals...))
			} else {
				q = q.Must(elastic.NewTermsQuery("claim_id.keyword", searchVals...))
			}
		}
	}

	if in.PublicKeyId != "" {
		value := hex.EncodeToString(base58.Decode(in.PublicKeyId)[1:21])
		q = q.Must(elastic.NewTermQuery("public_key_hash.keyword", value))
	}

	if in.HasChannelSignature != nil && in.HasChannelSignature.Value {
		q = q.Must(elastic.NewExistsQuery("signature_digest"))
		if in.SignatureValid != nil {
			q = q.Must(elastic.NewTermQuery("signature_valid", in.SignatureValid.Value))
		}
	} else if in.SignatureValid != nil {
		q = q.MinimumNumberShouldMatch(1)
		q = q.Should(elastic.NewBoolQuery().MustNot(elastic.NewExistsQuery("signature_digest")))
		q = q.Should(elastic.NewTermQuery("signature_valid", in.SignatureValid.Value))
	}

	if in.HasSource != nil {
		q = q.MinimumNumberShouldMatch(1)
		isStreamOrRepost := elastic.NewTermsQuery("claim_type", claimTypes["stream"], claimTypes["repost"])
		q = q.Should(elastic.NewBoolQuery().Must(isStreamOrRepost, elastic.NewMatchQuery("has_source", in.HasSource.Value)))
		q = q.Should(elastic.NewBoolQuery().MustNot(isStreamOrRepost))
		q = q.Should(elastic.NewBoolQuery().Must(elastic.NewTermQuery("reposted_claim_type", claimTypes["channel"])))
	}

	//var collapse *elastic.CollapseBuilder
	//if in.LimitClaimsPerChannel != nil {
	//	println(in.LimitClaimsPerChannel.Value)
	//	innerHit := elastic.
	//		NewInnerHit().
	//		//From(0).
	//		Size(int(in.LimitClaimsPerChannel.Value)).
	//		Name("channel_id")
	//	for _, x := range orderBy {
	//		innerHit = innerHit.Sort(x.Field, x.IsAsc)
	//	}
	//	collapse = elastic.NewCollapseBuilder("channel_id.keyword").InnerHit(innerHit)
	//}

	if in.TxNout != nil {
		q = q.Must(elastic.NewTermQuery("tx_nout", in.TxNout.Value))
	}

	q = AddTermsField(in.PublicKeyHash, "public_key_hash.keyword", q)
	q = AddTermsField(in.Author, "author.keyword", q)
	q = AddTermsField(in.Title, "title.keyword", q)
	q = AddTermsField(in.CanonicalUrl, "canonical_url.keyword", q)
	q = AddTermsField(in.ClaimName, "claim_name.keyword", q)
	q = AddTermsField(in.Description, "description.keyword", q)
	q = AddTermsField(in.MediaType, "media_type.keyword", q)
	q = AddTermsField(in.Normalized, "normalized.keyword", q)
	q = AddTermsField(in.PublicKeyBytes, "public_key_bytes.keyword", q)
	q = AddTermsField(in.ShortUrl, "short_url.keyword", q)
	q = AddTermsField(in.Signature, "signature.keyword", q)
	q = AddTermsField(in.SignatureDigest, "signature_digest.keyword", q)
	q = AddTermsField(in.TxId, "tx_id.keyword", q)
	q = AddTermsField(in.FeeCurrency, "fee_currency.keyword", q)
	q = AddTermsField(in.RepostedClaimId, "reposted_claim_id.keyword", q)


	q = AddTermsField(s.cleanTags(in.AnyTags), "tags.keyword", q)
	q = AddIndividualTermFields(s.cleanTags(in.AllTags), "tags.keyword", q, false)
	q = AddIndividualTermFields(s.cleanTags(in.NotTags), "tags.keyword", q, true)
	q = AddTermsField(in.AnyLanguages, "languages", q)
	q = AddIndividualTermFields(in.AllLanguages, "languages", q, false)

	q = AddInvertibleField(in.ChannelId, "channel_id.keyword", q)
	q = AddInvertibleField(in.ChannelIds, "channel_id.keyword", q)


	q = AddRangeField(in.TxPosition, "tx_position", q)
	q = AddRangeField(in.Amount, "amount", q)
	q = AddRangeField(in.Timestamp, "timestamp", q)
	q = AddRangeField(in.CreationTimestamp, "creation_timestamp", q)
	q = AddRangeField(in.Height, "height", q)
	q = AddRangeField(in.CreationHeight, "creation_height", q)
	q = AddRangeField(in.ActivationHeight, "activation_height", q)
	q = AddRangeField(in.ExpirationHeight, "expiration_height", q)
	q = AddRangeField(in.ReleaseTime, "release_time", q)
	q = AddRangeField(in.Reposted, "reposted", q)
	q = AddRangeField(in.FeeAmount, "fee_amount", q)
	q = AddRangeField(in.Duration, "duration", q)
	q = AddRangeField(in.CensorType, "censor_type", q)
	q = AddRangeField(in.ChannelJoin, "channel_join", q)
	q = AddRangeField(in.EffectiveAmount, "effective_amount", q)
	q = AddRangeField(in.SupportAmount, "support_amount", q)
	q = AddRangeField(in.TrendingGroup, "trending_group", q)
	q = AddRangeField(in.TrendingMixed, "trending_mixed", q)
	q = AddRangeField(in.TrendingLocal, "trending_local", q)
	q = AddRangeField(in.TrendingGlobal, "trending_global", q)

	if in.Text != "" {
		textQuery := elastic.NewSimpleQueryStringQuery(in.Text).
			FieldWithBoost("claim_name", 4).
			FieldWithBoost("channel_name", 8).
			FieldWithBoost("title", 1).
			FieldWithBoost("description", 0.5).
			FieldWithBoost("author", 1).
			FieldWithBoost("tags", 0.5)

		q = q.Must(textQuery)
	}


	//TODO make this only happen in dev environment
	indices, err := client.IndexNames()
	if err != nil {
		log.Fatalln(err)
	}
	var numIndices = 0
	if len(indices) > 0 {
		numIndices = len(indices) - 1
	}
	searchIndices := make([]string, numIndices)
	j := 0
	for i := 0; j < numIndices; i++ {
		if indices[i] == "claims" {
			continue
		}
		searchIndices[j] = indices[i]
		j = j + 1
	}

	fsc := elastic.NewFetchSourceContext(true).Exclude("description", "title")
	log.Printf("from: %d, size: %d\n", from, size)
	search := client.Search().
		Index(searchIndices...).
		FetchSourceContext(fsc).
		Query(q). // specify the query
		From(0).Size(1000)
	//if in.LimitClaimsPerChannel != nil {
	//	search = search.Collapse(collapse)
	//}
	for _, x := range orderBy {
		log.Println(x.Field, x.IsAsc)
		search = search.Sort(x.Field, x.IsAsc)
	}

	searchResult, err := search.Do(ctx) // execute
	if err != nil {
		return nil, err
	}

	log.Printf("%s: found %d results in %dms\n", in.Text, len(searchResult.Hits.Hits), searchResult.TookInMillis)

	var txos []*pb.Output
	var records []*record

	//if in.LimitClaimsPerChannel == nil {
	if true {
		records = make([]*record, 0, searchResult.TotalHits())

		var r record
		for _, item := range searchResult.Each(reflect.TypeOf(r)) {
			if t, ok := item.(record); ok {
				records = append(records, &t)
				//txos[i] = &pb.Output{
				//	TxHash: util.ToHash(t.Txid),
				//	Nout:   t.Nout,
				//	Height: t.Height,
				//}
			}
		}
	} else {
		records = make([]*record, 0, len(searchResult.Hits.Hits) * int(in.LimitClaimsPerChannel.Value))
		txos = make([]*pb.Output, 0, len(searchResult.Hits.Hits) * int(in.LimitClaimsPerChannel.Value))
		var i = 0
		for _, hit := range searchResult.Hits.Hits {
			if innerHit, ok := hit.InnerHits["channel_id"]; ok {
				for _, hitt := range innerHit.Hits.Hits {
					if i >= size {
						break
					}
					var t *record
					err := json.Unmarshal(hitt.Source, &t)
					if err != nil {
						return nil, err
					}
					records = append(records, t)
					i++
				}
			}
		}
		ms.Sort(records)
		log.Println(records)
		for _, t := range records {
			res := &pb.Output{
				TxHash: util.ToHash(t.Txid),
				Nout:   t.Nout,
				Height: t.Height,
			}
			txos = append(txos, res)
		}
	}

	var finalRecords []*record
	for _, rec := range records {
		log.Println(*rec)
	}


	log.Println("#########################")


	if in.LimitClaimsPerChannel != nil {
		finalRecords = searchAhead(records, pageSize, int(in.LimitClaimsPerChannel.Value))
		for _, rec := range finalRecords {
			log.Println(*rec)
		}
	} else {
		finalRecords = records
	}

	finalLength := int(math.Min(float64(len(finalRecords)), float64(pageSize)))
	// var start int = from
	txos = make([]*pb.Output, 0, finalLength)
	//for i, t := range finalRecords {
	j = 0
	for i := from; i < from + finalLength && i < len(finalRecords) && j < finalLength; i++ {
		t := finalRecords[i]
		res := &pb.Output{
			TxHash: util.ToHash(t.Txid),
			Nout:   t.Nout,
			Height: t.Height,
		}
		txos = append(txos, res)
		j += 1
	}

	// or if you want more control
	//for _, hit := range searchResult.Hits.Hits {
	//	// hit.Index contains the name of the index
	//
	//	var t map[string]interface{} // or could be a Record
	//	err := json.Unmarshal(hit.Source, &t)
	//	if err != nil {
	//		return nil, err
	//	}
	//
	//	b, err := json.MarshalIndent(t, "", "  ")
	//	if err != nil {
	//		fmt.Println("error:", err)
	//	}
	//	fmt.Println(string(b))
	//	//for k := range t {
	//	//	fmt.Println(k)
	//	//}
	//	//return nil, nil
	//}

	log.Printf("totalhits: %d\n", searchResult.TotalHits())
	return &pb.Outputs{
		Txos:   txos,
		Total:  uint32(searchResult.TotalHits()),
		Offset: uint32(int64(from) + searchResult.TotalHits()),
	}, nil
}

/*    def __search_ahead(self, search_hits: list, page_size: int, per_channel_per_page: int):
      reordered_hits = []
      channel_counters = Counter()
      next_page_hits_maybe_check_later = deque()
      while search_hits or next_page_hits_maybe_check_later:
          if reordered_hits and len(reordered_hits) % page_size == 0:
              channel_counters.clear()
          elif not reordered_hits:
              pass
          else:
              break  # means last page was incomplete and we are left with bad replacements
          for _ in range(len(next_page_hits_maybe_check_later)):
              claim_id, channel_id = next_page_hits_maybe_check_later.popleft()
              if per_channel_per_page > 0 and channel_counters[channel_id] < per_channel_per_page:
                  reordered_hits.append((claim_id, channel_id))
                  channel_counters[channel_id] += 1
              else:
                  next_page_hits_maybe_check_later.append((claim_id, channel_id))
          while search_hits:
              hit = search_hits.popleft()
              hit_id, hit_channel_id = hit['_id'], hit['_source']['channel_id']
              if hit_channel_id is None or per_channel_per_page <= 0:
                  reordered_hits.append((hit_id, hit_channel_id))
              elif channel_counters[hit_channel_id] < per_channel_per_page:
                  reordered_hits.append((hit_id, hit_channel_id))
                  channel_counters[hit_channel_id] += 1
                  if len(reordered_hits) % page_size == 0:
                      break
              else:
                  next_page_hits_maybe_check_later.append((hit_id, hit_channel_id))
      return reordered_hits

 */


func sumCounters(channelCounters map[string]int) int {
	var sum int = 0
	for _, v := range channelCounters {
		sum += v
	}

	return sum
}

func searchAhead(searchHits []*record, pageSize int, perChannelPerPage int) []*record {
	finalHits := make([]*record, 0 , len(searchHits))
	var channelCounters map[string]int
	channelCounters = make(map[string]int)
	nextPageHitsMaybeCheckLater := deque.New()
	searchHitsQ := deque.New()
	for _, rec := range searchHits {
		searchHitsQ.PushRight(rec)
	}
	for !searchHitsQ.Empty() || !nextPageHitsMaybeCheckLater.Empty() {
		if len(finalHits) > 0 && len(finalHits) % pageSize == 0 {
			channelCounters = make(map[string]int)
		} else if len(finalHits) != 0 {
			// means last page was incomplete and we are left with bad replacements
			break
		}

		// log.Printf("searchHitsQ = %d, nextPageHitsMaybeCheckLater = %d\n", searchHitsQ.Size(), nextPageHitsMaybeCheckLater.Size())

		for i := 0; i < nextPageHitsMaybeCheckLater.Size(); i++ {
			rec := nextPageHitsMaybeCheckLater.PopLeft().(*record)
			if perChannelPerPage > 0  && channelCounters[rec.ChannelId] < perChannelPerPage {
				finalHits = append(finalHits, rec)
				channelCounters[rec.ChannelId] = channelCounters[rec.ChannelId] + 1
			}
		}
		for !searchHitsQ.Empty() {
			hit := searchHitsQ.PopLeft().(*record)
			if hit.ChannelId == "" || perChannelPerPage < 0 {
				finalHits = append(finalHits, hit)
			} else if channelCounters[hit.ChannelId] < perChannelPerPage {
				finalHits = append(finalHits, hit)
				channelCounters[hit.ChannelId] = channelCounters[hit.ChannelId] + 1
				if len(finalHits) % pageSize == 0 {
					break
				}
			} else {
				nextPageHitsMaybeCheckLater.PushRight(hit)
			}
		}
	}
	return finalHits
}
