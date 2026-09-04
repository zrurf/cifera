/**
 * Cifera 代理运行时入口
 * 导入并注册所有拦截器
 */

export { rewriteUrl } from './rewriter';

// 初始化 Shadow Cookie Jar 托管（hook document.cookie）
import { initCookieManager } from './cookie';
initCookieManager();

// 注册所有拦截器（副作用导入）
import './interceptors/fetch';
import './interceptors/xhr';
import './interceptors/dom';
import './interceptors/worker';
import './interceptors/navigation';
import './interceptors/websocket';
