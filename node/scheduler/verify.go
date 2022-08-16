package scheduler

import (
	"context"
	"fmt"
	"sync"

	gocid "github.com/ipfs/go-cid"
	"github.com/linguohua/titan/api"
	"github.com/linguohua/titan/node/scheduler/db"
	"github.com/linguohua/titan/region"
)

// 边缘节点登录的时候
// 1.同个区域边缘节点组合成集群,每个集群的上行带宽为1G
// 选举过程
// 2.选举验证节点的时候,根据区域的边缘节点集群,看看这个区域需要多少个验证节点(要考虑下行宽带)
// 3.如果某个区域的验证节点不足,则需要再选出附近空闲的验证节点
// 验证过程
// 4.每个候选节点根据下行带宽,一次验证N个集群

const (
	groupPrefix     = "Group_"
	groupFullValMax = 1024
	groupFullValMin = 900
)

var (
	// 边缘节点组(根据区域分组,每个组上行带宽为1GB)
	edgeGroupMap sync.Map // {key:geo,val:map{key:groupID,val:map{key:deviceID,val:bandwidth}}}
	// 上行宽带未满的节点组
	groupLessFullMap sync.Map // {key:geo,val:map{key:groupID,val:bandwidth}}
	// 节点所在分组记录
	groupIDMap sync.Map // {key:deviceID,val:GroupID}
	groupCount int
)

func incrByGroupName() string {
	groupCount++

	return fmt.Sprintf("%s%d", groupPrefix, groupCount)
}

func edgeGrouping(node EdgeNode) string {
	deviceID := node.deviceInfo.DeviceId
	oldGroupID, ok := groupIDMap.Load(deviceID)
	if ok && oldGroupID != nil {
		g := oldGroupID.(string)
		return g
	}

	groupID := ""
	defer groupIDMap.Store(deviceID, groupID)

	geoKey := node.geoInfo.Geo
	bandwidth := node.bandwidth

	groups := loadGroupMap(geoKey)
	if groups != nil {
		// 看看有没有未满的组可以加入
		lessFulls := loadLessFullMap(geoKey)
		if lessFulls != nil {
			findGroupID := ""
			bandwidthT := 0
			for groupID, bandwidth := range lessFulls {
				bandwidthT = bandwidth + node.bandwidth
				if bandwidthT <= groupFullValMax {
					findGroupID = groupID
					break
				}
			}

			if findGroupID != "" {
				// 未满的组能加入
				group := groups[findGroupID]
				groupID = addGroup(geoKey, deviceID, findGroupID, bandwidth, group, lessFulls, groups)
			} else {
				// 未满的组不能加入
				groupID = addGroup(geoKey, deviceID, "", bandwidth, nil, lessFulls, groups)
			}
		} else {
			groupID = addGroup(geoKey, deviceID, "", bandwidth, nil, nil, groups)
		}
	} else {
		groupID = addGroup(geoKey, deviceID, "", bandwidth, nil, nil, nil)
	}

	return groupID
}

func addGroup(geoKey, deviceID, gName string, bandwidth int, group, lessFulls map[string]int, groups map[string]map[string]int) string {
	if group == nil {
		group = make(map[string]int)
		gName = incrByGroupName()
	}
	group[deviceID] = bandwidth

	if groups == nil {
		groups = make(map[string]map[string]int)
	}
	groups[gName] = group

	storeGroupMap(geoKey, groups)

	totalBandwidth := 0
	for _, bandwidth := range group {
		totalBandwidth += bandwidth
	}

	if lessFulls == nil {
		lessFulls = make(map[string]int)
	}

	if totalBandwidth < groupFullValMin {
		// 如果组内带宽未满 则保存到未满map
		lessFulls[gName] = totalBandwidth
		storeLessFullMap(geoKey, lessFulls)
	} else {
		delete(lessFulls, gName)
		storeLessFullMap(geoKey, lessFulls)
	}

	return gName
}

func loadGroupMap(geoKey string) map[string]map[string]int {
	groups, ok := edgeGroupMap.Load(geoKey)
	if ok && groups != nil {
		return groups.(map[string]map[string]int)
	}

	return nil
}

func storeGroupMap(geoKey string, val map[string]map[string]int) {
	edgeGroupMap.Store(geoKey, val)
}

func loadLessFullMap(geoKey string) map[string]int {
	groups, ok := groupLessFullMap.Load(geoKey)
	if ok && groups != nil {
		return groups.(map[string]int)
	}

	return nil
}

func storeLessFullMap(geoKey string, val map[string]int) {
	groupLessFullMap.Store(geoKey, val)
}

// PrintlnMap Println
func testPrintlnMap() {
	fmt.Println("edgeGroupMap--------------------------------")
	edgeGroupMap.Range(func(key, value interface{}) bool {
		g := key.(string)
		groups := value.(map[string]map[string]int)
		fmt.Println("geo:", g)

		for gID, group := range groups {
			bs := 0
			for _, b := range group {
				bs += b
			}
			fmt.Println("gId:", gID, ",group:", group, ",bandwidth:", bs)
		}

		return true
	})

	fmt.Println("groupLessFullMap--------------------------------")
	groupLessFullMap.Range(func(key, value interface{}) bool {
		g := key.(string)
		groups := value.(map[string]int)
		fmt.Println("geo:", g)

		for gID, bb := range groups {
			fmt.Println("gId:", gID, ",bb:", bb)
		}

		return true
	})
}

func spotCheck(candidate *CandidateNode, edges []*EdgeNode) {
	req := make([]api.ReqVarify, 0)
	result := make(map[string]string)

	for _, edge := range edges {
		// 查看节点缓存了哪些数据
		datas, err := db.GetCacheDB().GetCacheDataInfos(edge.deviceInfo.DeviceId)
		if err != nil {
			log.Warnf("spotCheck GetCacheDataInfos err:%v,DeviceId:%v", err.Error(), edge.deviceInfo.DeviceId)
			continue
		}

		if len(datas) <= 0 {
			continue
		}

		// random
		var cid string
		var tag string
		randomN := randomNum(0, len(datas))
		num := 0
		for c, t := range datas {
			cid = c
			tag = t

			if num == randomN {
				break
			}
			num++
		}

		req = append(req, api.ReqVarify{Fid: tag, URL: edge.addr})

		result[edge.deviceInfo.DeviceId] = cid
	}
	// 请求抽查
	varifyResults, err := candidate.nodeAPI.VerifyData(context.Background(), req)
	if err != nil {
		log.Warnf("VerifyData err:%v, DeviceId:%v", err.Error(), candidate.deviceInfo.DeviceId)
		return
	}

	// varify Result
	for _, varifyResult := range varifyResults {
		// TODO 判断带宽 超时时间等等
		// 结果判断
		c := result[varifyResult.DeviceID]

		// 判断CID
		gC, err := gocid.Decode(c)
		if err != nil {
			log.Warnf("Decode err:%v", err.Error())
			continue
		}

		gC = gocid.NewCidV1(gC.Type(), gC.Hash())

		vC, err := gocid.Decode(varifyResult.Cid)
		if err != nil {
			log.Warnf("Decode err:%v", err.Error())
			continue
		}

		vC = gocid.NewCidV1(vC.Type(), vC.Hash())

		cidOK := gC.Equals(vC)

		log.Infof("varifyResult candidate:%v , edge:%v ,eCid:%v,sCid:%v cidOK:%v", candidate.deviceInfo.DeviceId, varifyResult.DeviceID, varifyResult.Cid, c, cidOK)
		log.Infof("varifyResult vC:%v gC:%v", vC, gC)
		// TODO 写入DB 时间:候选节点:被验证节点:验证的cid:序号:结果
	}
}

// spot check edges
func spotChecks() error {
	// log.Infof("validatorCount:%v,candidateCount:%v", validatorCount, candidateCount)
	// find validators
	validators, err := db.GetCacheDB().GetValidatorsWithList()
	if err != nil {
		return err
	}

	useGroupID := make([]string, 0)

	for _, validatorID := range validators {
		geos, err := db.GetCacheDB().GetGeoWithValidatorList(validatorID)
		if err != nil {
			log.Warnf("GetGeoWithValidatorList err:%v,validatorID:%v", err.Error(), validatorID)
			continue
		}

		// edge list
		edges := make([]*EdgeNode, 0)

		// find edge
		for _, geo := range geos {
			groups := loadGroupMap(geo)
			if groups != nil {
				// rand group
				uID, deviceIDs := getNotAssignGroup(groups, useGroupID)
				for deviceID := range deviceIDs {
					edge := getEdgeNode(deviceID)
					if edge != nil {
						edges = append(edges, edge)
					}
				}
				useGroupID = uID
			} else {
				log.Warnf("spotChecks loadGroupMap is nil ,geo:%v", geo)
			}
		}

		validator := getCandidateNode(validatorID)
		if validator != nil {
			spotCheck(validator, edges)
		}

	}

	return nil
}

func getNotAssignGroup(groups map[string]map[string]int, usesID []string) ([]string, map[string]int) {
	for groupID, deviceIDs := range groups {
		for _, gID := range usesID {
			if groupID != gID {
				usesID = append(usesID, groupID)
				return usesID, deviceIDs
			}
		}
	}

	return usesID, nil
}

// 重置 验证者数据
func cleanValidators() error {
	validators, err := db.GetCacheDB().GetValidatorsWithList()
	if err != nil {
		return err
	}

	for _, validator := range validators {
		err = db.GetCacheDB().DelValidatorGeoList(validator)
		if err != nil {
			log.Warnf("DelValidatorGeoList err:%v, validator:%v", err.Error(), validator)
		}

		node := getCandidateNode(validator)
		if node != nil {
			node.isValidator = false
		}
	}

	err = db.GetCacheDB().DelValidatorList()
	if err != nil {
		return err
	}

	return nil
}

// 选举、分配验证者负责的区域
func electionValidators() error {
	testPrintlnMap()

	// 每个城市 选出X个验证者
	// 每隔Y时间 重新选举
	err := cleanValidators()
	if err != nil {
		return err
	}

	// 已被分配的验证者
	alreadyAssignValidatorMap := make(map[string]string)
	alreadyAssignValidatorList := make([]string, 0) // 带宽已经沾满的验证者
	// validatorBandwidthMap := make(map[string]int)   // 已用带宽
	// 未被分配到的边缘节点组数
	notAssignEdgeMap := make(map[string]int)

	edgeGroupMap.Range(func(key, value interface{}) bool {
		geo := key.(string)
		groups := value.(map[string]map[string]int)
		if groups == nil || len(groups) <= 0 {
			return true
		}

		gInfo := region.StringGeoToGeoInfo(geo)
		if gInfo == nil {
			log.Warnf("StringGeoToGeoInfo geo:%v", geo)
			return true
		}

		// 找出这个区域 所有候选节点
		cns, gLevel := findCandidateNodeWithGeo(*gInfo, []string{}, []string{})
		if gLevel == cityLevel && len(cns) > 0 {
			// 这里考虑 验证节点的下行带宽
			notAssignNum := len(groups)
			for _, c := range randomNums(0, len(cns), len(groups)) {
				notAssignNum--
				dID := cns[c].deviceInfo.DeviceId
				alreadyAssignValidatorMap[dID] = geo

				alreadyAssignValidatorList = append(alreadyAssignValidatorList, dID)
			}

			if notAssignNum > 0 {
				notAssignEdgeMap[geo] = notAssignNum
			}
		} else {
			notAssignEdgeMap[geo] = len(groups)
			return true
		}

		return true
	})

	candidateNotEnough := false
	// 处理第一轮未匹配到的边缘节点
	for len(notAssignEdgeMap) > 0 {
		for geo, num := range notAssignEdgeMap {
			gInfo := region.StringGeoToGeoInfo(geo)
			if gInfo == nil {
				log.Warnf("StringGeoToGeoInfo geo:%v", geo)
				continue
			}

			cns, _ := findCandidateNodeWithGeo(*gInfo, []string{}, alreadyAssignValidatorList)
			if len(cns) > 0 {
				for _, c := range randomNums(0, len(cns), num) {
					num--
					dID := cns[c].deviceInfo.DeviceId
					alreadyAssignValidatorMap[dID] = geo
					alreadyAssignValidatorList = append(alreadyAssignValidatorList, dID)
				}
				notAssignEdgeMap[geo] = num
				if num == 0 {
					delete(notAssignEdgeMap, geo)
				}
			} else {
				candidateNotEnough = true
			}
		}

		if candidateNotEnough {
			log.Warnf("Candidate Not Enough  assignEdge: %v", notAssignEdgeMap)
			break
		}
	}

	// 记录验证者负责的区域到redis
	for validator, geo := range alreadyAssignValidatorMap {
		getCandidateNode(validator).isValidator = true

		err = db.GetCacheDB().SetValidatorToList(validator)
		if err != nil {
			log.Warnf("SetValidatorToList err:%v, validator : %s", err.Error(), validator)
		}

		err = db.GetCacheDB().SetGeoToValidatorList(validator, geo)
		if err != nil {
			log.Warnf("SetGeoToValidatorList err:%v, validator : %s, geo : %s", err.Error(), validator, geo)
		}
	}

	// reset count
	resetCandidateAndValidatorCount()

	return nil
}