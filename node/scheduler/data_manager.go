package scheduler

import (
	"container/list"

	"github.com/linguohua/titan/api"
	"github.com/linguohua/titan/node/scheduler/db/cache"
	"github.com/linguohua/titan/node/scheduler/db/persistent"
)

// DataManager Data
type DataManager struct {
	nodeManager *NodeManager
	dataMap     map[string]*Data

	resultQueue   *list.List
	resultChannel chan bool
}

func newDataManager(nodeManager *NodeManager) *DataManager {
	d := &DataManager{
		nodeManager:   nodeManager,
		dataMap:       make(map[string]*Data),
		resultQueue:   list.New(),
		resultChannel: make(chan bool, 1),
	}

	go d.initChannelTask()

	return d
}

func (m *DataManager) findData(cid string) *Data {
	data, ok := m.dataMap[cid]
	if !ok {
		data = loadData(cid, m.nodeManager, m)
		if data != nil {
			m.dataMap[cid] = data
		}
		// return xerrors.New("already exists")
	}

	return data
}

func (m *DataManager) cacheData(cid string, reliability int) error {
	data, ok := m.dataMap[cid]
	if !ok {
		data = loadData(cid, m.nodeManager, m)
		if data == nil {
			data = newData(m.nodeManager, m, cid, reliability)
		}
		m.dataMap[cid] = data
		// return xerrors.New("already exists")
	}

	err := data.createCache(m)

	data.saveData()

	return err
}

func (m *DataManager) removeBlock(deviceID string, cids []string) {
	// TODO remove data info
	log.Errorf("removeBlock deviceID:%v,cids:%v", deviceID, cids)
}

// func (m *DataManager) cacheResult2(deviceID string, info api.CacheResultInfo) (string, string) {
// 	carfileID, cacheID := m.getCacheTask(deviceID)
// 	// log.Warnf("task carfileID:%v, cacheID:%v", carfileID, cacheID)
// 	if carfileID == "" {
// 		return "", ""
// 	}

// 	data := m.findData(carfileID)
// 	// log.Warnf("data:%v, ", data)
// 	if data != nil {
// 		return data.updateDataInfo(deviceID, cacheID, info)
// 	}

// 	return "", ""
// }

func (m *DataManager) addCacheTaskMap(deviceID, cid, cacheID string) {
	err := cache.GetDB().SetCacheDataTask(deviceID, cid, cacheID)
	if err != nil {
		log.Errorf("SetCacheDataTask err:%v", err.Error())
	}
}

func (m *DataManager) removeCacheTaskMap(deviceID string) {
	err := cache.GetDB().RemoveCacheDataTask(deviceID)
	if err != nil {
		log.Errorf("RemoveCacheDataTask err:%v", err.Error())
	}
}

func (m *DataManager) getCacheTask(deviceID string) (string, string) {
	return cache.GetDB().GetCacheDataTask(deviceID)
}

func (m *DataManager) initChannelTask() {
	for {
		<-m.resultChannel

		m.doUpdateCacheInfo()
	}
}

func (m *DataManager) writeChanWithSelect(b bool) {
	select {
	case m.resultChannel <- b:
		return
	default:
		// log.Warnf("channel blocked, can not write")
	}
}

func (m *DataManager) doUpdateCacheInfo() {
	for m.resultQueue.Len() > 0 {
		element := m.resultQueue.Front() // First element
		info := element.Value.(*api.CacheResultInfo)

		carfileID, cacheID := m.getCacheTask(info.DeviceID)
		// log.Warnf("task carfileID:%v, cacheID:%v", carfileID, cacheID)
		if carfileID != "" {
			data := m.findData(carfileID)
			// log.Warnf("data:%v, ", data)
			if data != nil {
				data.updateDataInfo(info.DeviceID, cacheID, info)
				// save to block table
				err := persistent.GetDB().SetCarfileInfo(info.DeviceID, info.Cid, carfileID, cacheID)
				if err != nil {
					log.Errorf("SetCarfileInfo err:%v,device:%v", err.Error(), info.DeviceID)
				}
			}
		}

		m.resultQueue.Remove(element) // Dequeue

		// time.Sleep(20 * time.Second)
		// v.writeChanWithSelect(true)
	}
}

func (m *DataManager) cacheResult(deviceID string, info *api.CacheResultInfo) error {
	info.DeviceID = deviceID
	m.resultQueue.PushBack(info)

	m.writeChanWithSelect(true)

	return nil
}
