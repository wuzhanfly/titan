package api

import "context"

type Locator interface {
	Common
	GetAccessPoints(ctx context.Context, deviceID string, securityKey string) ([]SchedulerAuth, error)                      //perm:read
	AddAccessPoints(ctx context.Context, areaID string, schedulerURL string, weight int, schedulerAccessToken string) error //perm:admin
	RemoveAccessPoints(ctx context.Context, areaID string) error                                                            //perm:admin                                  //perm:admin
	ListAccessPoints(ctx context.Context) (areaIDs []string, err error)                                                     //perm:admin
	ShowAccessPoint(ctx context.Context, areaID string) (AccessPoint, error)                                                //perm:admin

	DeviceOnline(ctx context.Context, deviceID string, areaID string, port int) error //perm:write
	DeviceOffline(ctx context.Context, deviceID string) error                         //perm:write

	GetDownloadInfosWithBlocks(ctx context.Context, cids []string) (map[string][]DownloadInfo, error) //perm:read
	GetDownloadInfoWithBlocks(ctx context.Context, cids []string) (map[string]DownloadInfo, error)    //perm:read
	GetDownloadInfoWithBlock(ctx context.Context, cid string) (DownloadInfo, error)                   //perm:read
}

type SchedulerAuth struct {
	URL         string
	AccessToken string
}

type SchedulerInfo struct {
	URL         string
	Weight      int
	Online      bool
	AccessToken string
}
type AccessPoint struct {
	AreaID         string
	SchedulerInfos []SchedulerInfo
}
