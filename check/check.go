package check

import (
	"genspark2api/common/config"
	logger "genspark2api/common/loggger"
)

func CheckEnvVariable() {
	logger.SysLog("environment variable checking...")

	if config.GSCookie == "" {
		logger.FatalLog("环境变量 GS_COOKIE 未设置")
	}
	logger.SysLog("environment variable check passed.")
}
