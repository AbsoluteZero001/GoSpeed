export namespace main {
	
	export class AppInfo {
	    version: string;
	    goVersion: string;
	    platform: string;
	    maxConnections: number;
	    defaultConnections: number;
	    defaultDurationMs: number;
	    defaultSampleIntervalMs: number;
	    minDurationMs: number;
	    maxDurationMs: number;
	    minSampleIntervalMs: number;
	    maxSampleIntervalMs: number;
	    latencySamples: number;
	    nodeConfigPath: string;
	    nodeConfigSource: string;
	    configSearchPaths: string[];
	    nodeCount: number;
	
	    static createFrom(source: any = {}) {
	        return new AppInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.version = source["version"];
	        this.goVersion = source["goVersion"];
	        this.platform = source["platform"];
	        this.maxConnections = source["maxConnections"];
	        this.defaultConnections = source["defaultConnections"];
	        this.defaultDurationMs = source["defaultDurationMs"];
	        this.defaultSampleIntervalMs = source["defaultSampleIntervalMs"];
	        this.minDurationMs = source["minDurationMs"];
	        this.maxDurationMs = source["maxDurationMs"];
	        this.minSampleIntervalMs = source["minSampleIntervalMs"];
	        this.maxSampleIntervalMs = source["maxSampleIntervalMs"];
	        this.latencySamples = source["latencySamples"];
	        this.nodeConfigPath = source["nodeConfigPath"];
	        this.nodeConfigSource = source["nodeConfigSource"];
	        this.configSearchPaths = source["configSearchPaths"];
	        this.nodeCount = source["nodeCount"];
	    }
	}
	export class NodeStatusView {
	    id: string;
	    name: string;
	    baseUrl: string;
	    protocol: string;
	    enabled: boolean;
	    local: boolean;
	    networkScope: string;
	    status: string;
	    httpRttMs?: number;
	    dnsMs?: number;
	    tcpMs?: number;
	    tlsMs?: number;
	    latencyMedianMs?: number;
	    latencySamples: number;
	    latencyBelowResolution: number;
	    latencyFailures: number;
	    attempts: number;
	    serverVersion?: string;
	    capabilities: boolean;
	    capabilitiesLegacy: boolean;
	    detail?: string;
	    checkedAt?: string;
	
	    static createFrom(source: any = {}) {
	        return new NodeStatusView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.baseUrl = source["baseUrl"];
	        this.protocol = source["protocol"];
	        this.enabled = source["enabled"];
	        this.local = source["local"];
	        this.networkScope = source["networkScope"];
	        this.status = source["status"];
	        this.httpRttMs = source["httpRttMs"];
	        this.dnsMs = source["dnsMs"];
	        this.tcpMs = source["tcpMs"];
	        this.tlsMs = source["tlsMs"];
	        this.latencyMedianMs = source["latencyMedianMs"];
	        this.latencySamples = source["latencySamples"];
	        this.latencyBelowResolution = source["latencyBelowResolution"];
	        this.latencyFailures = source["latencyFailures"];
	        this.attempts = source["attempts"];
	        this.serverVersion = source["serverVersion"];
	        this.capabilities = source["capabilities"];
	        this.capabilitiesLegacy = source["capabilitiesLegacy"];
	        this.detail = source["detail"];
	        this.checkedAt = source["checkedAt"];
	    }
	}
	export class NodeCheckResult {
	    path: string;
	    checkedAt: string;
	    cancelled: boolean;
	    nodes: NodeStatusView[];
	
	    static createFrom(source: any = {}) {
	        return new NodeCheckResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.checkedAt = source["checkedAt"];
	        this.cancelled = source["cancelled"];
	        this.nodes = this.convertValues(source["nodes"], NodeStatusView);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class NodeView {
	    id: string;
	    name: string;
	    baseUrl: string;
	    protocol: string;
	    enabled: boolean;
	    local: boolean;
	    provider?: string;
	    description?: string;
	    location: string;
	    networkScope: string;
	
	    static createFrom(source: any = {}) {
	        return new NodeView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.baseUrl = source["baseUrl"];
	        this.protocol = source["protocol"];
	        this.enabled = source["enabled"];
	        this.local = source["local"];
	        this.provider = source["provider"];
	        this.description = source["description"];
	        this.location = source["location"];
	        this.networkScope = source["networkScope"];
	    }
	}
	export class NodeListResult {
	    path: string;
	    source: string;
	    nodes: NodeView[];
	
	    static createFrom(source: any = {}) {
	        return new NodeListResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.source = source["source"];
	        this.nodes = this.convertValues(source["nodes"], NodeView);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	
	export class StartOptions {
	    selectionMode: string;
	    nodeId: string;
	    connections: number;
	    durationMs: number;
	    sampleIntervalMs: number;
	
	    static createFrom(source: any = {}) {
	        return new StartOptions(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.selectionMode = source["selectionMode"];
	        this.nodeId = source["nodeId"];
	        this.connections = source["connections"];
	        this.durationMs = source["durationMs"];
	        this.sampleIntervalMs = source["sampleIntervalMs"];
	    }
	}
	export class StartTestResult {
	    started: boolean;
	    mode: string;
	    message?: string;
	
	    static createFrom(source: any = {}) {
	        return new StartTestResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.started = source["started"];
	        this.mode = source["mode"];
	        this.message = source["message"];
	    }
	}
	export class StatusResult {
	    busy: boolean;
	    kind?: string;
	    state: string;
	    message?: string;
	
	    static createFrom(source: any = {}) {
	        return new StatusResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.busy = source["busy"];
	        this.kind = source["kind"];
	        this.state = source["state"];
	        this.message = source["message"];
	    }
	}

}

