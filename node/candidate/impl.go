package candidate

import (
	"context"
	"fmt"

	"github.com/linguohua/titan/api"
	"github.com/linguohua/titan/api/client"

	"github.com/linguohua/titan/node/common"

	"github.com/ipfs/go-cid"
	"github.com/ipfs/go-datastore"
	logging "github.com/ipfs/go-log/v2"
	mh "github.com/multiformats/go-multihash"
)

var log = logging.Logger("main")

func NewLocalCandidateNode(ds datastore.Batching, scheduler api.Scheduler) api.Candidate {
	return CandidateAPI{ds: ds, scheduler: scheduler}
}

func cidFromData(data []byte) (string, error) {
	pref := cid.Prefix{
		Version:  1,
		Codec:    uint64(cid.Raw),
		MhType:   mh.SHA2_256,
		MhLength: -1, // default length
	}

	c, err := pref.Sum(data)
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("%v", c), nil
}

type CandidateAPI struct {
	common.CommonAPI
	ds        datastore.Batching
	scheduler api.Scheduler
}

func (candidate CandidateAPI) WaitQuiet(ctx context.Context) error {
	return nil
}

func (candidate CandidateAPI) VerifyData(ctx context.Context, fid string, url string) (string, error) {
	edgeAPI, closer, err := client.NewEdge(ctx, url, nil)
	if err != nil {
		log.Errorf("VerifyData NewEdge err : %v", err)
		return "", err
	}

	defer closer()

	data, err := edgeAPI.LoadDataByVerifier(ctx, fid)
	if err != nil {
		log.Errorf("VerifyData LoadDataByVerifier err : %v", err)
		return "", err
	}

	return cidFromData(data)
}
