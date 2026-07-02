export namespace config {
	
	export class AuthConfig {
	    username: string;
	    password: string;
	
	    static createFrom(source: any = {}) {
	        return new AuthConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.username = source["username"];
	        this.password = source["password"];
	    }
	}
	export class HopConfig {
	    user: string;
	    host: string;
	    port: number;
	    auth_type: string;
	    password?: string;
	    key_path?: string;
	    passphrase?: string;
	
	    static createFrom(source: any = {}) {
	        return new HopConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.user = source["user"];
	        this.host = source["host"];
	        this.port = source["port"];
	        this.auth_type = source["auth_type"];
	        this.password = source["password"];
	        this.key_path = source["key_path"];
	        this.passphrase = source["passphrase"];
	    }
	}
	export class LocalForward {
	    id: string;
	    local_port: number;
	    remote_host: string;
	    remote_port: number;
	    allow_external: boolean;
	
	    static createFrom(source: any = {}) {
	        return new LocalForward(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.local_port = source["local_port"];
	        this.remote_host = source["remote_host"];
	        this.remote_port = source["remote_port"];
	        this.allow_external = source["allow_external"];
	    }
	}
	export class TLSConfig {
	    cert_file: string;
	    key_file: string;
	
	    static createFrom(source: any = {}) {
	        return new TLSConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.cert_file = source["cert_file"];
	        this.key_file = source["key_file"];
	    }
	}
	export class ProxyListener {
	    id: string;
	    protocol: string;
	    listen_port: number;
	    allow_external: boolean;
	    auth?: AuthConfig;
	    tls?: TLSConfig;
	
	    static createFrom(source: any = {}) {
	        return new ProxyListener(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.protocol = source["protocol"];
	        this.listen_port = source["listen_port"];
	        this.allow_external = source["allow_external"];
	        this.auth = this.convertValues(source["auth"], AuthConfig);
	        this.tls = this.convertValues(source["tls"], TLSConfig);
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
	    id: string;
	    name: string;
	    hop_chain: HopConfig[];
	    tunnel_type: string;
	    local_forwards?: LocalForward[];
	    proxy_listeners?: ProxyListener[];
	    auto_reconnect: boolean;
	    status: string;
	
	    static createFrom(source: any = {}) {
	        return new TunnelConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.hop_chain = this.convertValues(source["hop_chain"], HopConfig);
	        this.tunnel_type = source["tunnel_type"];
	        this.local_forwards = this.convertValues(source["local_forwards"], LocalForward);
	        this.proxy_listeners = this.convertValues(source["proxy_listeners"], ProxyListener);
	        this.auto_reconnect = source["auto_reconnect"];
	        this.status = source["status"];
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

export namespace logger {
	
	export class Entry {
	    // Go type: time
	    time: any;
	    level: string;
	    message: string;
	    tunnel_id?: string;
	
	    static createFrom(source: any = {}) {
	        return new Entry(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.time = this.convertValues(source["time"], null);
	        this.level = source["level"];
	        this.message = source["message"];
	        this.tunnel_id = source["tunnel_id"];
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

