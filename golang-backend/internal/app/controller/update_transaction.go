package controller

import (
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"network-panel/golang-backend/internal/app/response"
	"network-panel/golang-backend/internal/panelupgrade"
)

// VersionUpgrade queues an independent native transaction. Never mutate a Docker image.
// @Summary 启动原生事务升级（202 仅表示受理）
// @Tags version
// @Accept json
// @Produce json
// @Param data body SwaggerVersionUpgradeReq true "固定发布标签与可选下载代理"
// @Success 202 {object} SwaggerVersionUpgradeResp
// @Failure 409 {object} BaseSwaggerResp
// @Router /api/v1/version/upgrade [post]
func VersionUpgrade(c *gin.Context) {
	if panelupgrade.Container() {
		c.JSON(http.StatusConflict, response.R{Code: -1, Msg: "Docker 部署必须通过镜像升级，禁止在线覆盖容器内程序", Data: gin.H{"state": "IMAGE_UPGRADE_REQUIRED"}})
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	var request panelupgrade.Request
	if err := c.ShouldBindJSON(&request); err != nil && !errors.Is(err, io.EOF) {
		c.JSON(http.StatusBadRequest, response.ErrMsg("无效升级请求"))
		return
	}
	if err := panelupgrade.ValidateRequest(&request); err != nil {
		c.JSON(http.StatusBadRequest, response.ErrMsg(err.Error()))
		return
	}
	status, err := panelupgrade.Start(c.Request.Context(), request)
	if err != nil {
		code := http.StatusConflict
		c.JSON(code, response.R{Code: -1, Msg: err.Error(), Data: status})
		return
	}
	c.JSON(http.StatusAccepted, response.Ok(status))
}

// VersionUpgradeStatus reads one durable transaction; only COMMITTED means success.
// @Summary 查询升级事务状态
// @Tags version
// @Produce json
// @Param id query string true "事务 ID"
// @Success 200 {object} SwaggerVersionUpgradeResp
// @Router /api/v1/version/upgrade-status [get]
func VersionUpgradeStatus(c *gin.Context) {
	status, err := panelupgrade.LoadStatus(c.Query("id"))
	if err != nil {
		code := http.StatusNotFound
		if strings.Contains(err.Error(), "无效事务") {
			code = http.StatusBadRequest
		}
		c.JSON(code, response.ErrMsg("升级事务状态不可用: "+err.Error()))
		return
	}
	c.JSON(http.StatusOK, response.Ok(status))
}
