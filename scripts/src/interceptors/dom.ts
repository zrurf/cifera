/**
 * DOM API 拦截器
 * 拦截 setAttribute、property 赋值、MutationObserver 等
 */

import { rewriteUrl } from '../rewriter';

// 通用 URL 属性名集合（适用于所有标签）
const URL_ATTRIBUTES = new Set([
    'src', 'href', 'action', 'poster', 'srcset',
    'cite', 'longdesc', 'profile', 'usemap', 'codebase', 'archive', 'background',
]);

// 标签特定的 URL 属性白名单
// 某些属性（如 data）仅在特定标签中才是 URL，其他标签中是普通数据
const TAG_SPECIFIC_URL_ATTRS: Record<string, Set<string>> = {
    'object': new Set(['data', 'src']),
    'applet': new Set(['data', 'src']),
    'embed': new Set(['data', 'src']),
    'iframe': new Set(['src']),
    'frame': new Set(['src']),
    'img': new Set(['src', 'srcset']),
    'script': new Set(['src']),
    'link': new Set(['href']),
    'a': new Set(['href']),
    'area': new Set(['href']),
    'base': new Set(['href']),
    'form': new Set(['action']),
    'input': new Set(['src']),
    'video': new Set(['src', 'poster']),
    'audio': new Set(['src']),
    'source': new Set(['src', 'srcset']),
    'track': new Set(['src']),
    'body': new Set(['background']),
    'table': new Set(['background']),
    'td': new Set(['background']),
    'th': new Set(['background']),
    'tr': new Set(['background']),
    'blockquote': new Set(['cite']),
    'q': new Set(['cite']),
    'ins': new Set(['cite']),
    'del': new Set(['cite']),
};

/**
 * 判断在指定标签中，某个属性是否为 URL 属性
 */
function isUrlAttr(tagName: string, attrName: string): boolean {
    const lowerAttr = attrName.toLowerCase();
    const lowerTag = tagName.toLowerCase();

    // data-* 自定义属性永远不是 URL
    if (lowerAttr.startsWith('data-')) {
        return false;
    }

    // 如果标签有特定的属性白名单，只改写白名单中的属性
    if (TAG_SPECIFIC_URL_ATTRS[lowerTag]) {
        return TAG_SPECIFIC_URL_ATTRS[lowerTag].has(lowerAttr);
    }

    // 标签没有特定白名单，使用通用规则
    return URL_ATTRIBUTES.has(lowerAttr);
}

/**
 * 获取某个标签的所有 URL 属性名
 * 用于 MutationObserver 的 attributeFilter
 */
function getUrlAttrsForTag(tagName: string): string[] {
    const lowerTag = tagName.toLowerCase();
    const tagSpecific = TAG_SPECIFIC_URL_ATTRS[lowerTag];
    if (tagSpecific) {
        return Array.from(tagSpecific);
    }
    return Array.from(URL_ATTRIBUTES);
}

// 需要拦截 property 的元素映射
const PROPERTY_DESCRIPTORS: Array<{
    proto: any;
    prop: string;
    attr: string;
}> = [
    { proto: HTMLImageElement.prototype, prop: 'src', attr: 'src' },
    { proto: HTMLScriptElement.prototype, prop: 'src', attr: 'src' },
    { proto: HTMLLinkElement.prototype, prop: 'href', attr: 'href' },
    { proto: HTMLIFrameElement.prototype, prop: 'src', attr: 'src' },
    { proto: HTMLSourceElement.prototype, prop: 'src', attr: 'src' },
    { proto: HTMLVideoElement.prototype, prop: 'src', attr: 'src' },
    { proto: HTMLAudioElement.prototype, prop: 'src', attr: 'src' },
    { proto: HTMLTrackElement.prototype, prop: 'src', attr: 'src' },
    { proto: HTMLEmbedElement.prototype, prop: 'src', attr: 'src' },
    { proto: HTMLObjectElement.prototype, prop: 'data', attr: 'data' },
    { proto: HTMLFormElement.prototype, prop: 'action', attr: 'action' },
    { proto: HTMLAnchorElement.prototype, prop: 'href', attr: 'href' },
    { proto: HTMLAreaElement.prototype, prop: 'href', attr: 'href' },
    { proto: HTMLBaseElement.prototype, prop: 'href', attr: 'href' },
    { proto: HTMLInputElement.prototype, prop: 'src', attr: 'src' },
];

/**
 * 拦截 Element.prototype.setAttribute
 */
const originalSetAttribute = Element.prototype.setAttribute;

Element.prototype.setAttribute = function(name: string, value: string): void {
    if (isUrlAttr(this.tagName, name)) {
        try {
            value = rewriteUrl(value);
        } catch {
            // 改写失败，保持原样
        }
    }
    return originalSetAttribute.call(this, name, value);
};

/**
 * 拦截 property descriptor（如 img.src = '...'）
 */
function interceptPropertyDescriptors(): void {
    for (const { proto, prop } of PROPERTY_DESCRIPTORS) {
        const descriptor = Object.getOwnPropertyDescriptor(proto, prop);
        if (!descriptor || !descriptor.set) continue;

        const originalSetter = descriptor.set;
        const originalGetter = descriptor.get;

        Object.defineProperty(proto, prop, {
            set(this: HTMLElement, value: string) {
                try {
                    value = rewriteUrl(value);
                } catch {
                    // 改写失败，保持原样
                }
                originalSetter.call(this, value);
            },
            get(this: HTMLElement) {
                return originalGetter?.call(this);
            },
            configurable: true,
            enumerable: true,
        });
    }
}

interceptPropertyDescriptors();

/**
 * 改写单个元素的 URL 属性（基于标签感知白名单）
 */
function rewriteElementUrls(el: Element): void {
    if (!(el instanceof HTMLElement)) return;

    const tagName = el.tagName.toLowerCase();
    const tagSpecific = TAG_SPECIFIC_URL_ATTRS[tagName];

    // 确定需要检查的属性集合
    const attrsToCheck: Set<string> = tagSpecific
        ? new Set([...tagSpecific])
        : new Set(URL_ATTRIBUTES);

    for (const attr of attrsToCheck) {
        const value = el.getAttribute(attr);
        if (value) {
            try {
                const rewritten = rewriteUrl(value);
                if (rewritten !== value) {
                    originalSetAttribute.call(el, attr, rewritten);
                }
            } catch {
                // 改写失败，保持原样
            }
        }
    }
}

/**
 * 递归改写元素及其子元素的 URL
 */
function rewriteElementTreeUrls(el: Element): void {
    rewriteElementUrls(el);
    const children = el.children;
    for (let i = 0; i < children.length; i++) {
        rewriteElementTreeUrls(children[i]);
    }
}

// 合并所有需要监控的属性名（通用 + 所有标签特定的）
const allUrlAttrs = new Set(URL_ATTRIBUTES);
for (const tagAttrs of Object.values(TAG_SPECIFIC_URL_ATTRS)) {
    for (const attr of tagAttrs) {
        allUrlAttrs.add(attr);
    }
}

/**
 * 使用 MutationObserver 监控 DOM 变更，改写新增元素的 URL
 */
const observer = new MutationObserver((mutations: MutationRecord[]) => {
    for (const mutation of mutations) {
        // 处理新增节点
        if (mutation.type === 'childList') {
            for (const node of mutation.addedNodes) {
                if (node instanceof HTMLElement) {
                    rewriteElementTreeUrls(node);
                }
            }
        }
        // 处理属性变更
        if (mutation.type === 'attributes' && mutation.target instanceof HTMLElement) {
            const attrName = mutation.attributeName;
            if (attrName && isUrlAttr(mutation.target.tagName, attrName)) {
                const value = mutation.target.getAttribute(attrName);
                if (value) {
                    try {
                        const rewritten = rewriteUrl(value);
                        if (rewritten !== value) {
                            originalSetAttribute.call(mutation.target, attrName, rewritten);
                        }
                    } catch {
                        // 改写失败，保持原样
                    }
                }
            }
        }
    }
});

// 等待 DOM 准备就绪后开始观察
if (document.documentElement) {
    observer.observe(document.documentElement, {
        childList: true,
        subtree: true,
        attributes: true,
        attributeFilter: Array.from(allUrlAttrs),
    });
} else {
    document.addEventListener('DOMContentLoaded', () => {
        if (document.documentElement) {
            observer.observe(document.documentElement, {
                childList: true,
                subtree: true,
                attributes: true,
                attributeFilter: Array.from(allUrlAttrs),
            });
        }
    });
}
