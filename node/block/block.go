package block

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"strconv"
	"sync"
	"time"

	blocks "github.com/ipfs/go-block-format"
	"github.com/ipfs/go-cid"
	"github.com/ipfs/go-datastore"
	"github.com/ipfs/go-datastore/query"
	exchange "github.com/ipfs/go-ipfs-exchange-interface"
	"github.com/ipfs/go-merkledag"
	dagpb "github.com/ipld/go-codec-dagpb"
	"github.com/ipld/go-ipld-prime/node/basicnode"
	"github.com/linguohua/titan/api"
	"github.com/linguohua/titan/blockstore"
	"github.com/linguohua/titan/node/helper"

	format "github.com/ipfs/go-ipld-format"
	legacy "github.com/ipfs/go-ipld-legacy"
	logging "github.com/ipfs/go-log/v2"
)

var log = logging.Logger("block")

type delayReq struct {
	blockInfo api.BlockInfo
	count     int
	// use for edge node load block
	candidateURL string
	carFileCid   string
	CacheID      string
}

type blockStat struct {
	cid        string
	fid        string
	links      []string
	blockSize  int
	linksSize  uint64
	carFileCid string
	CacheID    string
}

type Block struct {
	ds            datastore.Batching
	blockStore    blockstore.BlockStore
	scheduler     api.Scheduler
	reqList       []*delayReq
	cachingList   []*delayReq
	saveBlockLock *sync.Mutex
	reqListLock   *sync.Mutex
	block         BlockInterface
	deviceID      string
	exchange      exchange.Interface
	blockLoaderCh chan bool
	ipfsGateway   string
}

// TODO need to rename
type BlockInterface interface {
	loadBlocks(block *Block, req []*delayReq)
}

func NewBlock(ds datastore.Batching, blockStore blockstore.BlockStore, scheduler api.Scheduler, blockInterface BlockInterface, ipfsGateway, deviceID string) *Block {
	block := &Block{
		ds:         ds,
		blockStore: blockStore,
		scheduler:  scheduler,
		block:      blockInterface,
		exchange:   nil,
		deviceID:   deviceID,

		saveBlockLock: &sync.Mutex{},
		reqListLock:   &sync.Mutex{},
		blockLoaderCh: make(chan bool),
		ipfsGateway:   ipfsGateway,
	}

	go block.startBlockLoader()

	legacy.RegisterCodec(cid.DagProtobuf, dagpb.Type.PBNode, merkledag.ProtoNodeConverter)
	legacy.RegisterCodec(cid.Raw, basicnode.Prototype.Bytes, merkledag.RawNodeConverter)

	return block
}

func apiReq2DelayReq(req *api.ReqCacheData) []*delayReq {
	results := make([]*delayReq, 0, len(req.BlockInfos))
	for _, blockInfo := range req.BlockInfos {
		if len(blockInfo.Cid) == 0 {
			continue
		}

		req := &delayReq{blockInfo: blockInfo, count: 0, candidateURL: req.CandidateURL, carFileCid: req.CardFileCid, CacheID: req.CacheID}
		results = append(results, req)
	}

	return results
}

func (block *Block) startBlockLoader() {
	if block.block == nil {
		log.Panic("block.block == nil")
	}

	for {
		<-block.blockLoaderCh
		block.doLoadBlock()
	}
}

func (block *Block) notifyBlockLoader() {
	select {
	case block.blockLoaderCh <- true:
	default:
	}
}

func (block *Block) dequeue(len int) []*delayReq {
	block.reqListLock.Lock()
	defer block.reqListLock.Unlock()
	reqs := block.reqList[:len]
	block.reqList = block.reqList[len:]
	return reqs

}

func (block *Block) enqueue(delayReqs []*delayReq) {
	block.reqListLock.Lock()
	defer block.reqListLock.Unlock()
	block.reqList = append(block.reqList, delayReqs...)

}

func (block *Block) doLoadBlock() {
	for len(block.reqList) > 0 {
		doLen := len(block.reqList)
		if doLen > helper.Batch {
			doLen = helper.Batch
		}

		doReqs := block.dequeue(doLen)
		block.cachingList = doReqs

		block.block.loadBlocks(block, doReqs)
		block.cachingList = nil
	}
}

func (block *Block) addReq2WaitList(delayReqs []*delayReq) {
	block.enqueue(delayReqs)
	block.notifyBlockLoader()
}

func (block *Block) cacheResultWithError(ctx context.Context, bStat blockStat, err error) {
	log.Errorf("cacheResultWithError, cid:%s, fid:%s, cacheID:%s, carFileID:%s, error:%v", bStat.cid, bStat.fid, bStat.CacheID, bStat.carFileCid, err)
	block.cacheResult(ctx, "", err, bStat)
}

func (block *Block) cacheResult(ctx context.Context, from string, err error, bStat blockStat) {
	errMsg := ""
	success := true
	if err != nil {
		success = false
		errMsg = err.Error()
	}

	result := api.CacheResultInfo{
		Cid:        bStat.cid,
		IsOK:       success,
		Msg:        errMsg,
		From:       from,
		Links:      bStat.links,
		BlockSize:  bStat.blockSize,
		LinksSize:  bStat.linksSize,
		CarFileCid: bStat.carFileCid,
		CacheID:    bStat.CacheID,
		Fid:        bStat.fid,
	}

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	_, err = block.scheduler.CacheResult(ctx, block.deviceID, result)
	if err != nil {
		log.Errorf("cacheResult CacheResult error:%v", err)
		return
	}

	// if success && fid != "" {
	// 	oldCid, _ := block.getCID(fid)
	// 	if len(oldCid) != 0 && oldCid != bStat.cid {
	// 		log.Infof("cacheResult delete old cid:%s, new cid:%s", oldCid, bStat.cid)
	// 		err = block.ds.Delete(ctx, helper.NewKeyCID(oldCid))
	// 		if err != nil {
	// 			log.Errorf("cacheResult, delete key fid %s error:%v", fid, err)
	// 		}
	// 	}

	// 	oldFid, _ := block.getFID(bStat.cid)
	// 	if oldFid != "" {
	// 		// delete old fid key
	// 		log.Infof("cacheResult delete old fid:%s, new fid:%s", oldFid, fid)
	// 		err = block.ds.Delete(ctx, helper.NewKeyFID(oldFid))
	// 		if err != nil {
	// 			log.Errorf("cacheResult, delete key fid %s error:%v", fid, err)
	// 		}
	// 	}

	// 	err = block.ds.Put(ctx, helper.NewKeyFID(fid), []byte(bStat.cid))
	// 	if err != nil {
	// 		log.Errorf("cacheResult save fid error:%v", err)
	// 	}

	// 	err = block.ds.Put(ctx, helper.NewKeyCID(bStat.cid), []byte(fid))
	// 	if err != nil {
	// 		log.Errorf("cacheResult save cid error:%v", err)
	// 	}

	// }
}

func (block *Block) filterAvailableReq(reqs []*delayReq) []*delayReq {
	ctx := context.Background()

	from := ""
	results := make([]*delayReq, 0, len(reqs))
	for _, reqData := range reqs {
		// target, err := cid.Decode(reqData.Cid)
		// if err != nil {
		// 	log.Errorf("loadBlocksAsync failed to decode CID %v", err)
		// 	continue
		// }

		// // convert cid to v0
		// if target.Version() != 0 && target.Type() == cid.DagProtobuf {
		// 	target = cid.NewCidV0(target.Hash())
		// }

		cidStr := fmt.Sprintf("%s", reqData.blockInfo.Cid)

		buf, err := block.blockStore.Get(cidStr)
		if err == nil {
			fid, _ := block.getFID(reqData.blockInfo.Cid)
			if fid != reqData.blockInfo.Fid {
				block.updateCidAndFid(ctx, reqData.blockInfo.Cid, reqData.blockInfo.Fid)
			}

			links, err := getLinks(block, buf, cidStr)
			if err != nil {
				log.Errorf("filterAvailableReq getLinks error:%s", err.Error())
				continue
			}

			linksSize := uint64(0)
			cids := make([]string, 0, len(links))
			for _, link := range links {
				cids = append(cids, link.Cid.String())
				linksSize += link.Size
			}

			bStat := blockStat{cid: cidStr, fid: reqData.blockInfo.Fid, links: cids, blockSize: len(buf), linksSize: linksSize, carFileCid: reqData.carFileCid, CacheID: reqData.CacheID}
			block.cacheResult(ctx, from, nil, bStat)
			continue
		}

		results = append(results, reqData)
	}

	return results
}

func (block *Block) CacheBlocks(ctx context.Context, req api.ReqCacheData) error {
	log.Infof("CacheBlocks, req carFileCid:%s, cacheID:%s, candidate_url:%s, cid len:%d", req.CardFileCid, req.CacheID, req.CandidateURL, len(req.BlockInfos))
	// delayReq := block.filterAvailableReq(apiReq2DelayReq(&req))
	// if len(delayReq) == 0 {
	// 	log.Debug("CacheData, len(req) == 0 not need to handle")
	// 	return nil
	// }
	block.addReq2WaitList(apiReq2DelayReq(&req))
	return nil
}

// delete block in local store and scheduler
func (block *Block) DeleteBlocks(ctx context.Context, cids []string) ([]api.BlockOperationResult, error) {
	log.Infof("DeleteBlocks, cids len:%d", len(cids))
	// delResult := api.DelResult{}
	results := make([]api.BlockOperationResult, 0)

	if block.blockStore == nil {
		log.Errorf("DeleteBlocks, blockStore not setting")
		return results, fmt.Errorf("edge.blockStore == nil")
	}

	for _, cid := range cids {
		block.deleteFidAndCid(cid)
		err := block.blockStore.Delete(cid)
		if err == datastore.ErrNotFound {
			log.Infof("DeleteBlocks cid %s not exist", cid)
			continue
		}

		if err != nil {
			result := api.BlockOperationResult{Cid: cid, ErrMsg: err.Error()}
			results = append(results, result)
			log.Errorf("DeleteBlocks, delete block %s error:%v", cid, err)
			continue
		}
	}
	return results, nil
}

// told to scheduler, local block was delete
func (block *Block) AnnounceBlocksWasDelete(ctx context.Context, cids []string) ([]api.BlockOperationResult, error) {
	log.Debug("AnnounceBlocksWasDelete")
	// delResult := api.DelResult{}
	failedResults := make([]api.BlockOperationResult, 0)

	result, err := block.scheduler.DeleteBlockRecords(ctx, block.deviceID, cids)
	if err != nil {
		log.Errorf("AnnounceBlocksWasDelete, delete block error:%v", err)
		return failedResults, err
	}

	for _, cid := range cids {
		_, ok := result[cid]
		if ok {
			continue
		}

		err = block.blockStore.Delete(cid)
		if err != nil {
			result[cid] = err.Error()
		}
		block.deleteFidAndCid(cid)
	}

	for k, v := range result {
		log.Errorf("AnnounceBlocksWasDelete, delete block %s error:%v", k, v)
		result := api.BlockOperationResult{Cid: k, ErrMsg: v}
		failedResults = append(failedResults, result)
	}

	return failedResults, nil
}

func (block *Block) QueryCacheStat(ctx context.Context) (api.CacheStat, error) {
	result := api.CacheStat{}

	keyCount, err := block.blockStore.KeyCount()
	if err != nil {
		log.Errorf("block store key count error:%v", err)
	}

	result.CacheBlockCount = keyCount
	result.WaitCacheBlockNum = len(block.reqList)
	result.DoingCacheBlockNum = len(block.cachingList)

	log.Infof("CacheBlockCount:%d,WaitCacheBlockNum:%d, DoingCacheBlockNum:%d", result.CacheBlockCount, result.WaitCacheBlockNum, result.DoingCacheBlockNum)
	return result, nil
}

func (block *Block) BlockStoreStat(ctx context.Context) error {
	log.Debug("BlockStoreStat")

	return nil
}

func (block *Block) QueryCachingBlocks(ctx context.Context) (api.CachingBlockList, error) {
	result := api.CachingBlockList{}
	return result, nil
}

func (block *Block) LoadBlock(ctx context.Context, cid string) ([]byte, error) {
	// log.Infof("LoadBlock, cid:%s", cid)
	if block.blockStore == nil {
		log.Errorf("LoadData, blockStore not setting")
		return nil, nil
	}

	return block.blockStore.Get(cid)
}

func (block *Block) GetAllCidsFromBlockStore() ([]string, error) {
	return block.blockStore.GetAllKeys()
}

func (block *Block) DeleteAllBlocks(ctx context.Context) error {
	return block.deleteAllBlocks()
}

func (block *Block) GetCID(ctx context.Context, fid string) (string, error) {
	return block.getCID(fid)
}

func (block *Block) GetFID(ctx context.Context, cid string) (string, error) {
	return block.getFID(cid)
}

func (block *Block) LoadBlockWithFid(fid string) ([]byte, error) {
	cid, err := block.getCID(fid)
	if err != nil {
		return nil, err
	}

	return block.blockStore.Get(cid)
}

func (block *Block) GetBlockStoreCheckSum(ctx context.Context) (string, error) {
	return block.getBlockStoreCheckSum()
}

func (block *Block) ScrubBlocks(ctx context.Context, scrub api.ScrubBlocks) error {
	return nil
}

func (block *Block) getCID(fid string) (string, error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	value, err := block.ds.Get(ctx, helper.NewKeyFID(fid))
	if err != nil {
		// log.Errorf("Get cid from store error:%v, fid:%s", err, fid)
		return "", err
	}

	return string(value), nil
}

func (block *Block) getFID(cid string) (string, error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	value, err := block.ds.Get(ctx, helper.NewKeyCID(cid))
	if err != nil {
		// log.Errorf("Get fid from store error:%v, cid:%s", err, cid)
		return "", err
	}

	return string(value), nil
}

func (block *Block) deleteFidAndCid(cid string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	value, err := block.ds.Get(ctx, helper.NewKeyCID(cid))
	if err != nil {
		return err
	}

	fid := string(value)

	err = block.ds.Delete(ctx, helper.NewKeyFID(fid))
	if err != nil {
		return err
	}

	err = block.ds.Delete(ctx, helper.NewKeyCID(cid))
	if err != nil {
		return err
	}

	return nil
}

func (block *Block) getMaxFid() (string, error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q := query.Query{Prefix: "fid", Orders: []query.Order{query.OrderByKeyDescending{}}}
	results, err := block.ds.Query(ctx, q)
	if err != nil {
		return "", err
	}

	result := results.Next()
	r := <-result
	log.Infof("last key:%s, value:%s", r.Key, string(r.Value))

	return r.Key, nil
}

func (block *Block) deleteAllBlocks() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	q := query.Query{Prefix: "fid"}
	results, err := block.ds.Query(ctx, q)
	if err != nil {
		log.Errorf("deleteAllBlocks error:%s", err.Error())
		return err
	}

	result := results.Next()
	for {
		r, ok := <-result
		if !ok {
			log.Info("delete all block complete")
			return nil
		}

		_, err = block.AnnounceBlocksWasDelete(ctx, []string{string(r.Value)})
		if err != nil {
			log.Infof("err:%v, cid:%s", err, string(r.Value))
		}
		log.Infof("deleteAllBlocks key:%s", r.Key)
	}
}

func (block *Block) getBlockStoreCheckSum() (string, error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	q := query.Query{Prefix: "fid"}
	results, err := block.ds.Query(ctx, q)
	if err != nil {
		log.Errorf("deleteAllBlocks error:%s", err.Error())
		return "", err
	}
	var cidCollection string
	result := results.Next()
	for {
		r, ok := <-result
		if !ok {
			break
		}

		cidCollection += string(r.Value)
	}

	hasher := md5.New()
	hasher.Write([]byte(cidCollection))
	hash := hasher.Sum(nil)

	return hex.EncodeToString(hash), nil
}

func (block *Block) scrubBlockStore(scrub api.ScrubBlocks) error {
	startFid, err := strconv.Atoi(scrub.StartFid)
	if err != nil {
		log.Errorf("scrubBlockStore parse  error:%s", err.Error())
		return err
	}

	endFid, err := strconv.Atoi(scrub.EndFix)
	if err != nil {
		log.Errorf("scrubBlockStore error:%s", err.Error())
		return err
	}

	need2DeleteBlocks := make([]string, 0)
	blocks := scrub.Blocks
	for i := startFid; i <= endFid; i++ {
		fid := fmt.Sprintf("%d", i)
		cid, err := block.getCID(fid)
		if err == datastore.ErrNotFound {
			continue
		}

		_, ok := blocks[fid]
		if ok {
			delete(blocks, fid)
		} else {
			need2DeleteBlocks = append(need2DeleteBlocks, cid)
		}
	}

	// delete blocks that not exist on scheduler
	for _, cid := range need2DeleteBlocks {
		err = block.deleteFidAndCid(cid)
		if err != nil {
			log.Errorf("deleteFidAndCid error:%s", err.Error())
		}

		err = block.blockStore.Delete(cid)
		if err != nil {
			log.Errorf("deleteFidAndCid error:%s", err.Error())
		}
	}

	// TODO: download block that not exist in local

	return nil
}

func (block *Block) resolveLinks(blk blocks.Block) ([]*format.Link, error) {
	ctx := context.Background()

	node, err := legacy.DecodeNode(ctx, blk)
	if err != nil {
		log.Error("resolveLinks err:%v", err)
		return make([]*format.Link, 0), err
	}

	return node.Links(), nil
}

func (block *Block) saveBlock(ctx context.Context, data []byte, cid, fid string) error {
	block.saveBlockLock.Lock()
	defer block.saveBlockLock.Unlock()

	log.Infof("saveBlock fid:%s, cid:%s", fid, cid)
	err := block.blockStore.Put(cid, data)
	if err != nil {
		return err
	}

	return block.updateCidAndFid(ctx, cid, fid)
}

func (block *Block) updateCidAndFid(ctx context.Context, cid, fid string) error {
	// delete old fid relate cid
	oldCid, _ := block.getCID(fid)
	if len(oldCid) > 0 && oldCid != cid {
		block.ds.Delete(ctx, helper.NewKeyCID(oldCid))
		log.Errorf("Fid %s aready exist, and relate cid %s will be delete", fid, oldCid)
	}
	// delete old cid relate fid
	oldFid, _ := block.getFID(cid)
	if len(oldFid) > 0 && oldFid != fid {
		block.ds.Delete(ctx, helper.NewKeyCID(oldFid))
		log.Errorf("Cid %s aready exist, and relate fid %s will be delete", cid, oldFid)
	}

	err := block.ds.Put(ctx, helper.NewKeyFID(fid), []byte(cid))
	if err != nil {
		return err
	}

	err = block.ds.Put(ctx, helper.NewKeyCID(cid), []byte(fid))
	if err != nil {
		return err
	}

	return nil
}

func getLinks(block *Block, data []byte, cidStr string) ([]*format.Link, error) {
	if len(data) == 0 {
		return make([]*format.Link, 0), nil
	}

	target, err := cid.Decode(cidStr)
	if err != nil {
		return make([]*format.Link, 0), err
	}

	blk, err := blocks.NewBlockWithCid(data, target)
	if err != nil {
		return make([]*format.Link, 0), err
	}

	return block.resolveLinks(blk)
}
