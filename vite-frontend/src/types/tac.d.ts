// TAC验证码SDK的TypeScript类型声明

declare global {
  interface Window {
    TAC: typeof TAC;
    currentCaptcha?: any;
    currentCaptchaRes?: any;
  }
}

interface CaptchaConfig {
  // 生成接口 (必选项,必须配置)
  requestCaptchaDataUrl: string;
  // 验证接口 (必选项,必须配置)
  validCaptchaUrl: string;
  // 验证码绑定的div块 (必选项,必须配置)
  bindEl: string;
  // 验证成功回调函数(必选项,必须配置)
  validSuccess: (res: any, captcha: any, tac: any) => void;
  // 验证失败的回调函数(可选)
  validFail?: (res: any, captcha: any, tac: any) => void;
  // 请求头配置(可选)
  requestHeaders?: Record<string, string>;
  // 关闭按钮回调(可选)
  btnCloseFun?: (event: any, tac: any) => void;
  // 刷新按钮回调(可选)
  btnRefreshFun?: (event: any, tac: any) => void;
  // 时间转时间戳(可选)
  timeToTimestamp?: boolean;
}

interface CaptchaStyle {
  // 按钮样式URL(可选)
  btnUrl?: string;
  // 背景样式URL(可选)
  bgUrl?: string;
  // logo地址URL(可选)
  logoUrl?: string | null;
  // 滑动边框背景色(可选)
  moveTrackMaskBgColor?: string;
  // 滑动边框颜色(可选)
  moveTrackMaskBorderColor?: string;
}

declare class TAC {
  constructor(config: CaptchaConfig, style?: CaptchaStyle);

  init(): TAC;
  reloadCaptcha(): void;
  destroyWindow(): void;
  openCaptcha(): void;
}

export {};
