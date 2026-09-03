export namespace app {
	
	export class HopConfig {
	    node_id: string;
	    endpoint: string;
	    node_wg_pubkey: string;
	    tunnel_ip: string;
	    bytes_allowed: number;
	
	    static createFrom(source: any = {}) {
	        return new HopConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.node_id = source["node_id"];
	        this.endpoint = source["endpoint"];
	        this.node_wg_pubkey = source["node_wg_pubkey"];
	        this.tunnel_ip = source["tunnel_ip"];
	        this.bytes_allowed = source["bytes_allowed"];
	    }
	}
	export class HubStatus {
	    url: string;
	    name: string;
	    version: string;
	    keyset_id: string;
	    balance_sats: number;
	    node_count: number;
	
	    static createFrom(source: any = {}) {
	        return new HubStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.url = source["url"];
	        this.name = source["name"];
	        this.version = source["version"];
	        this.keyset_id = source["keyset_id"];
	        this.balance_sats = source["balance_sats"];
	        this.node_count = source["node_count"];
	    }
	}
	export class Invoice {
	    quote_id: string;
	    bolt11: string;
	    payment_hash: string;
	    amount_sats: number;
	    // Go type: time
	    expires_at: any;
	
	    static createFrom(source: any = {}) {
	        return new Invoice(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.quote_id = source["quote_id"];
	        this.bolt11 = source["bolt11"];
	        this.payment_hash = source["payment_hash"];
	        this.amount_sats = source["amount_sats"];
	        this.expires_at = this.convertValues(source["expires_at"], null);
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
	export class TunnelConfig {
	    entry: HopConfig;
	    exit: HopConfig;
	    client_key: string;
	
	    static createFrom(source: any = {}) {
	        return new TunnelConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.entry = this.convertValues(source["entry"], HopConfig);
	        this.exit = this.convertValues(source["exit"], HopConfig);
	        this.client_key = source["client_key"];
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
	export class Session {
	    state: string;
	    config: TunnelConfig;
	    spent_sats: number;
	    // Go type: time
	    started_at: any;
	
	    static createFrom(source: any = {}) {
	        return new Session(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.state = source["state"];
	        this.config = this.convertValues(source["config"], TunnelConfig);
	        this.spent_sats = source["spent_sats"];
	        this.started_at = this.convertValues(source["started_at"], null);
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

}

export namespace main {
	
	export class StatusView {
	    unlocked: boolean;
	    hub_url: string;
	    state: string;
	    balance_sats: number;
	    tunnel_ready: boolean;
	    tunnel_error?: string;
	    error?: string;
	
	    static createFrom(source: any = {}) {
	        return new StatusView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.unlocked = source["unlocked"];
	        this.hub_url = source["hub_url"];
	        this.state = source["state"];
	        this.balance_sats = source["balance_sats"];
	        this.tunnel_ready = source["tunnel_ready"];
	        this.tunnel_error = source["tunnel_error"];
	        this.error = source["error"];
	    }
	}

}

export namespace types {
	
	export class NodeInfo {
	    id: string;
	    nostr_pubkey: string;
	    wg_pubkey: string;
	    endpoint: string;
	    connect_url?: string;
	    lnurl: string;
	    deposit_sats: number;
	    upload_mbps: number;
	    download_mbps: number;
	    load: number;
	    capacity: number;
	    role: string;
	    version: string;
	
	    static createFrom(source: any = {}) {
	        return new NodeInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.nostr_pubkey = source["nostr_pubkey"];
	        this.wg_pubkey = source["wg_pubkey"];
	        this.endpoint = source["endpoint"];
	        this.connect_url = source["connect_url"];
	        this.lnurl = source["lnurl"];
	        this.deposit_sats = source["deposit_sats"];
	        this.upload_mbps = source["upload_mbps"];
	        this.download_mbps = source["download_mbps"];
	        this.load = source["load"];
	        this.capacity = source["capacity"];
	        this.role = source["role"];
	        this.version = source["version"];
	    }
	}

}

