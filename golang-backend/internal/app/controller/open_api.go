package controller

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"network-panel/golang-backend/internal/app/model"
	"network-panel/golang-backend/internal/app/response"
	"network-panel/golang-backend/internal/app/util"
	dbpkg "network-panel/golang-backend/internal/db"
)

// GET /api/v1/open_api/sub_store?user=...&pwd=...&tunnel=-1|id
func OpenAPISubStore(c *gin.Context) {
	user := c.Query("user")
	pwd := c.Query("pwd")
	tunnel := c.DefaultQuery("tunnel", "-1")
	if user == "" {
		c.JSON(http.StatusOK, response.ErrMsg("用户不能为空"))
		return
	}
	if pwd == "" {
		c.JSON(http.StatusOK, response.ErrMsg("密码不能为空"))
		return
	}
	var u model.User
	if err := dbpkg.DB.Where("user = ?", user).First(&u).Error; err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("鉴权失败"))
		return
	}
	if u.Pwd != util.MD5(pwd) {
		c.JSON(http.StatusOK, response.ErrMsg("鉴权失败"))
		return
	}

	const GIGA int64 = 1024 * 1024 * 1024
	var header string
	if tunnel == "-1" {
		header = buildSubHeader(u.OutFlow, u.InFlow, u.Flow*GIGA, valOr0(u.ExpTime)/1000)
	} else {
		tid, _ := strconv.ParseInt(tunnel, 10, 64)
		var ut model.UserTunnel
		if err := dbpkg.DB.First(&ut, tid).Error; err != nil || ut.UserID != u.ID {
			c.JSON(http.StatusOK, response.ErrMsg("隧道不存在"))
			return
		}
		header = buildSubHeader(ut.OutFlow, ut.InFlow, ut.Flow*GIGA, valOr0(ut.ExpTime)/1000)
	}
	c.Header("subscription-userinfo", header)
	c.JSON(http.StatusOK, header)
}

func buildSubHeader(upload, download, total, expire int64) string {
	return "upload=" + strconv.FormatInt(download, 10) + "; download=" + strconv.FormatInt(upload, 10) + "; total=" + strconv.FormatInt(total, 10) + "; expire=" + strconv.FormatInt(expire, 10)
}

func valOr0(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}
