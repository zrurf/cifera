/**
 * Cifera 代理运行时入口
 * 导入并注册所有拦截器
 */

// 导出 URL 改写函数供外部使用
export { rewriteUrl } from './rewriter';

// 注册所有拦截器（副作用导入）
import './interceptors/fetch';
import './interceptors/xhr';
import './interceptors/dom';
import './interceptors/worker';
import './interceptors/navigation';
