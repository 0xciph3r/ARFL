export namespace app {
	
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

}

export namespace main {
	
	export class StatusView {
	    unlocked: boolean;
	    hub_url: string;
	    state: string;
	    balance_sats: number;
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

