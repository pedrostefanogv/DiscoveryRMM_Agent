export namespace main {
	
	export class APIWorkflowState {
	    id: string;
	    name: string;
	    color: string;
	    isInitial: boolean;
	    isFinal: boolean;
	    displayOrder: number;
	
	    static createFrom(source: any = {}) {
	        return new APIWorkflowState(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.color = source["color"];
	        this.isInitial = source["isInitial"];
	        this.isFinal = source["isFinal"];
	        this.displayOrder = source["displayOrder"];
	    }
	}
	export class APITicket {
	    id: string;
	    title: string;
	    description: string;
	    priority: number;
	    category?: string;
	    agentId?: string;
	    clientId: string;
	    siteId?: string;
	    createdAt: string;
	    workflowState?: APIWorkflowState;
	    rating?: number;
	    ratedAt?: string;
	    ratedBy?: string;
	
	    static createFrom(source: any = {}) {
	        return new APITicket(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.title = source["title"];
	        this.description = source["description"];
	        this.priority = source["priority"];
	        this.category = source["category"];
	        this.agentId = source["agentId"];
	        this.clientId = source["clientId"];
	        this.siteId = source["siteId"];
	        this.createdAt = source["createdAt"];
	        this.workflowState = this.convertValues(source["workflowState"], APIWorkflowState);
	        this.rating = source["rating"];
	        this.ratedAt = source["ratedAt"];
	        this.ratedBy = source["ratedBy"];
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
	
	export class AgentInfo {
	    agentId: string;
	    clientId: string;
	    siteId: string;
	    hostname: string;
	    displayName: string;
	
	    static createFrom(source: any = {}) {
	        return new AgentInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.agentId = source["agentId"];
	        this.clientId = source["clientId"];
	        this.siteId = source["siteId"];
	        this.hostname = source["hostname"];
	        this.displayName = source["displayName"];
	    }
	}
	export class AgentStatus {
	    connected: boolean;
	    agentId: string;
	    server: string;
	    lastEvent: string;
	
	    static createFrom(source: any = {}) {
	        return new AgentStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.connected = source["connected"];
	        this.agentId = source["agentId"];
	        this.server = source["server"];
	        this.lastEvent = source["lastEvent"];
	    }
	}
	export class AutomationExecutionView {
	    executionId: string;
	    commandId?: string;
	    taskId?: string;
	    taskName?: string;
	    actionType?: string;
	    actionLabel?: string;
	    installationType?: string;
	    installationLabel?: string;
	    sourceType?: string;
	    sourceLabel?: string;
	    triggerType?: string;
	    triggerLabel?: string;
	    status: string;
	    statusLabel: string;
	    success: boolean;
	    exitCode: number;
	    exitCodeSet: boolean;
	    errorMessage?: string;
	    output?: string;
	    packageId?: string;
	    scriptId?: string;
	    correlationId?: string;
	    startedAt?: string;
	    finishedAt?: string;
	    metadataJson?: string;
	    durationLabel?: string;
	    summaryLine?: string;
	    hasPendingCallback: boolean;
	
	    static createFrom(source: any = {}) {
	        return new AutomationExecutionView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.executionId = source["executionId"];
	        this.commandId = source["commandId"];
	        this.taskId = source["taskId"];
	        this.taskName = source["taskName"];
	        this.actionType = source["actionType"];
	        this.actionLabel = source["actionLabel"];
	        this.installationType = source["installationType"];
	        this.installationLabel = source["installationLabel"];
	        this.sourceType = source["sourceType"];
	        this.sourceLabel = source["sourceLabel"];
	        this.triggerType = source["triggerType"];
	        this.triggerLabel = source["triggerLabel"];
	        this.status = source["status"];
	        this.statusLabel = source["statusLabel"];
	        this.success = source["success"];
	        this.exitCode = source["exitCode"];
	        this.exitCodeSet = source["exitCodeSet"];
	        this.errorMessage = source["errorMessage"];
	        this.output = source["output"];
	        this.packageId = source["packageId"];
	        this.scriptId = source["scriptId"];
	        this.correlationId = source["correlationId"];
	        this.startedAt = source["startedAt"];
	        this.finishedAt = source["finishedAt"];
	        this.metadataJson = source["metadataJson"];
	        this.durationLabel = source["durationLabel"];
	        this.summaryLine = source["summaryLine"];
	        this.hasPendingCallback = source["hasPendingCallback"];
	    }
	}
	export class AutomationTaskView {
	    commandId?: string;
	    taskId: string;
	    name: string;
	    description?: string;
	    actionType: string;
	    actionLabel: string;
	    installationType?: string;
	    installationLabel?: string;
	    packageId?: string;
	    scriptId?: string;
	    scriptName?: string;
	    scriptVersion?: string;
	    scriptType?: string;
	    scriptTypeLabel?: string;
	    commandPayload?: string;
	    scopeType: string;
	    scopeLabel: string;
	    requiresApproval: boolean;
	    triggerImmediate: boolean;
	    triggerRecurring: boolean;
	    triggerOnUserLogin: boolean;
	    triggerOnAgentCheckIn: boolean;
	    scheduleCron?: string;
	    includeTags?: string[];
	    excludeTags?: string[];
	    lastUpdatedAt?: string;
	
	    static createFrom(source: any = {}) {
	        return new AutomationTaskView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.commandId = source["commandId"];
	        this.taskId = source["taskId"];
	        this.name = source["name"];
	        this.description = source["description"];
	        this.actionType = source["actionType"];
	        this.actionLabel = source["actionLabel"];
	        this.installationType = source["installationType"];
	        this.installationLabel = source["installationLabel"];
	        this.packageId = source["packageId"];
	        this.scriptId = source["scriptId"];
	        this.scriptName = source["scriptName"];
	        this.scriptVersion = source["scriptVersion"];
	        this.scriptType = source["scriptType"];
	        this.scriptTypeLabel = source["scriptTypeLabel"];
	        this.commandPayload = source["commandPayload"];
	        this.scopeType = source["scopeType"];
	        this.scopeLabel = source["scopeLabel"];
	        this.requiresApproval = source["requiresApproval"];
	        this.triggerImmediate = source["triggerImmediate"];
	        this.triggerRecurring = source["triggerRecurring"];
	        this.triggerOnUserLogin = source["triggerOnUserLogin"];
	        this.triggerOnAgentCheckIn = source["triggerOnAgentCheckIn"];
	        this.scheduleCron = source["scheduleCron"];
	        this.includeTags = source["includeTags"];
	        this.excludeTags = source["excludeTags"];
	        this.lastUpdatedAt = source["lastUpdatedAt"];
	    }
	}
	export class AutomationStateView {
	    available: boolean;
	    connected: boolean;
	    loadedFromCache: boolean;
	    upToDate: boolean;
	    includeScriptContent: boolean;
	    policyFingerprint?: string;
	    generatedAt?: string;
	    lastSyncAt?: string;
	    lastAttemptAt?: string;
	    lastError?: string;
	    correlationId?: string;
	    taskCount: number;
	    pendingCallbacks: number;
	    tasks?: AutomationTaskView[];
	    recentExecutions?: AutomationExecutionView[];
	
	    static createFrom(source: any = {}) {
	        return new AutomationStateView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.available = source["available"];
	        this.connected = source["connected"];
	        this.loadedFromCache = source["loadedFromCache"];
	        this.upToDate = source["upToDate"];
	        this.includeScriptContent = source["includeScriptContent"];
	        this.policyFingerprint = source["policyFingerprint"];
	        this.generatedAt = source["generatedAt"];
	        this.lastSyncAt = source["lastSyncAt"];
	        this.lastAttemptAt = source["lastAttemptAt"];
	        this.lastError = source["lastError"];
	        this.correlationId = source["correlationId"];
	        this.taskCount = source["taskCount"];
	        this.pendingCallbacks = source["pendingCallbacks"];
	        this.tasks = this.convertValues(source["tasks"], AutomationTaskView);
	        this.recentExecutions = this.convertValues(source["recentExecutions"], AutomationExecutionView);
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
	
	export class ChatConfig {
	    endpoint: string;
	    apiKey: string;
	    model: string;
	    systemPrompt: string;
	    maxTokens: number;
	
	    static createFrom(source: any = {}) {
	        return new ChatConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.endpoint = source["endpoint"];
	        this.apiKey = source["apiKey"];
	        this.model = source["model"];
	        this.systemPrompt = source["systemPrompt"];
	        this.maxTokens = source["maxTokens"];
	    }
	}
	export class ChatMessage {
	    role: string;
	    content: string;
	
	    static createFrom(source: any = {}) {
	        return new ChatMessage(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.role = source["role"];
	        this.content = source["content"];
	    }
	}
	export class CloseTicketInput {
	    rating?: number;
	    comment?: string;
	    workflowStateId?: string;
	
	    static createFrom(source: any = {}) {
	        return new CloseTicketInput(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.rating = source["rating"];
	        this.comment = source["comment"];
	        this.workflowStateId = source["workflowStateId"];
	    }
	}
	export class CreateTicketInput {
	    title: string;
	    description: string;
	    priority: number;
	    category: string;
	
	    static createFrom(source: any = {}) {
	        return new CreateTicketInput(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.title = source["title"];
	        this.description = source["description"];
	        this.priority = source["priority"];
	        this.category = source["category"];
	    }
	}
	export class DebugConfig {
	    apiScheme: string;
	    apiServer: string;
	    authToken: string;
	    natsServer: string;
	    agentId: string;
	    scheme?: string;
	    server?: string;
	
	    static createFrom(source: any = {}) {
	        return new DebugConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.apiScheme = source["apiScheme"];
	        this.apiServer = source["apiServer"];
	        this.authToken = source["authToken"];
	        this.natsServer = source["natsServer"];
	        this.agentId = source["agentId"];
	        this.scheme = source["scheme"];
	        this.server = source["server"];
	    }
	}
	export class KnowledgeArticle {
	    id: string;
	    title: string;
	    category: string;
	    summary: string;
	    content: string;
	    tags: string[];
	    author: string;
	    scope: string;
	    publishedAt: string;
	    difficulty: string;
	    readTimeMin: number;
	    updatedAt: string;
	
	    static createFrom(source: any = {}) {
	        return new KnowledgeArticle(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.title = source["title"];
	        this.category = source["category"];
	        this.summary = source["summary"];
	        this.content = source["content"];
	        this.tags = source["tags"];
	        this.author = source["author"];
	        this.scope = source["scope"];
	        this.publishedAt = source["publishedAt"];
	        this.difficulty = source["difficulty"];
	        this.readTimeMin = source["readTimeMin"];
	        this.updatedAt = source["updatedAt"];
	    }
	}
	export class RealtimeStatus {
	    natsConnected: boolean;
	    signalrConnectedAgents: number;
	    // Go type: time
	    checkedAtUtc: any;
	
	    static createFrom(source: any = {}) {
	        return new RealtimeStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.natsConnected = source["natsConnected"];
	        this.signalrConnectedAgents = source["signalrConnectedAgents"];
	        this.checkedAtUtc = this.convertValues(source["checkedAtUtc"], null);
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
	export class RuntimeFlags {
	    debugMode: boolean;
	
	    static createFrom(source: any = {}) {
	        return new RuntimeFlags(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.debugMode = source["debugMode"];
	    }
	}
	export class StatusOverview {
	    connected: boolean;
	    connectionLabel: string;
	    hostname: string;
	    server: string;
	    connectionType: string;
	    appVersion: string;
	    osName: string;
	    osVersion: string;
	    lastInventoryCollected: string;
	    realtimeAvailable: boolean;
	    realtimeNatsConnected: boolean;
	    realtimeConnectedAgents: number;
	    realtimeMessage: string;
	    // Go type: time
	    checkedAtUtc: any;
	
	    static createFrom(source: any = {}) {
	        return new StatusOverview(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.connected = source["connected"];
	        this.connectionLabel = source["connectionLabel"];
	        this.hostname = source["hostname"];
	        this.server = source["server"];
	        this.connectionType = source["connectionType"];
	        this.appVersion = source["appVersion"];
	        this.osName = source["osName"];
	        this.osVersion = source["osVersion"];
	        this.lastInventoryCollected = source["lastInventoryCollected"];
	        this.realtimeAvailable = source["realtimeAvailable"];
	        this.realtimeNatsConnected = source["realtimeNatsConnected"];
	        this.realtimeConnectedAgents = source["realtimeConnectedAgents"];
	        this.realtimeMessage = source["realtimeMessage"];
	        this.checkedAtUtc = this.convertValues(source["checkedAtUtc"], null);
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
	export class TicketComment {
	    id: string;
	    author: string;
	    content: string;
	    isInternal: boolean;
	    createdAt: string;
	
	    static createFrom(source: any = {}) {
	        return new TicketComment(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.author = source["author"];
	        this.content = source["content"];
	        this.isInternal = source["isInternal"];
	        this.createdAt = source["createdAt"];
	    }
	}

}

export namespace mcp {
	
	export class Registry {
	
	
	    static createFrom(source: any = {}) {
	        return new Registry(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	
	    }
	}

}

export namespace models {
	
	export class AppItem {
	    id: string;
	    name: string;
	    publisher: string;
	    version: string;
	    description: string;
	    homepage: string;
	    license: string;
	    tags: string[];
	    installCommand: string;
	    category: string;
	    icon: string;
	    lastUpdated: string;
	
	    static createFrom(source: any = {}) {
	        return new AppItem(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.publisher = source["publisher"];
	        this.version = source["version"];
	        this.description = source["description"];
	        this.homepage = source["homepage"];
	        this.license = source["license"];
	        this.tags = source["tags"];
	        this.installCommand = source["installCommand"];
	        this.category = source["category"];
	        this.icon = source["icon"];
	        this.lastUpdated = source["lastUpdated"];
	    }
	}
	export class AutoexecItem {
	    path: string;
	    name: string;
	    source: string;
	
	    static createFrom(source: any = {}) {
	        return new AutoexecItem(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.name = source["name"];
	        this.source = source["source"];
	    }
	}
	export class BatteryInfo {
	    manufacturer: string;
	    model: string;
	    serialNumber: string;
	    cycleCount: number;
	    state: string;
	    charging: boolean;
	    charged: boolean;
	    designedCapacityMAh: number;
	    maxCapacityMAh: number;
	    currentCapacityMAh: number;
	    percentRemaining: number;
	    amperageMA: number;
	    voltageMV: number;
	    minutesUntilEmpty: number;
	    minutesToFullCharge: number;
	    chemistry: string;
	    health: string;
	    condition: string;
	    manufactureDateEpoch: number;
	
	    static createFrom(source: any = {}) {
	        return new BatteryInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.manufacturer = source["manufacturer"];
	        this.model = source["model"];
	        this.serialNumber = source["serialNumber"];
	        this.cycleCount = source["cycleCount"];
	        this.state = source["state"];
	        this.charging = source["charging"];
	        this.charged = source["charged"];
	        this.designedCapacityMAh = source["designedCapacityMAh"];
	        this.maxCapacityMAh = source["maxCapacityMAh"];
	        this.currentCapacityMAh = source["currentCapacityMAh"];
	        this.percentRemaining = source["percentRemaining"];
	        this.amperageMA = source["amperageMA"];
	        this.voltageMV = source["voltageMV"];
	        this.minutesUntilEmpty = source["minutesUntilEmpty"];
	        this.minutesToFullCharge = source["minutesToFullCharge"];
	        this.chemistry = source["chemistry"];
	        this.health = source["health"];
	        this.condition = source["condition"];
	        this.manufactureDateEpoch = source["manufactureDateEpoch"];
	    }
	}
	export class BitLockerInfo {
	    deviceId: string;
	    driveLetter: string;
	    persistentVolumeId: string;
	    conversionStatus: number;
	    protectionStatus: number;
	    encryptionMethod: string;
	    version: number;
	    percentageEncrypted: number;
	    lockStatus: number;
	
	    static createFrom(source: any = {}) {
	        return new BitLockerInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.deviceId = source["deviceId"];
	        this.driveLetter = source["driveLetter"];
	        this.persistentVolumeId = source["persistentVolumeId"];
	        this.conversionStatus = source["conversionStatus"];
	        this.protectionStatus = source["protectionStatus"];
	        this.encryptionMethod = source["encryptionMethod"];
	        this.version = source["version"];
	        this.percentageEncrypted = source["percentageEncrypted"];
	        this.lockStatus = source["lockStatus"];
	    }
	}
	export class CPUFeature {
	    feature: string;
	    value: string;
	    outputRegister: string;
	    outputBit: number;
	    inputEAX: string;
	
	    static createFrom(source: any = {}) {
	        return new CPUFeature(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.feature = source["feature"];
	        this.value = source["value"];
	        this.outputRegister = source["outputRegister"];
	        this.outputBit = source["outputBit"];
	        this.inputEAX = source["inputEAX"];
	    }
	}
	export class CPUInfo {
	    deviceId: string;
	    model: string;
	    manufacturer: string;
	    processorType: string;
	    cpuStatus: number;
	    numberOfCores: number;
	    logicalProcessors: number;
	    addressWidth: number;
	    currentClockSpeed: number;
	    maxClockSpeed: number;
	    socketDesignation: string;
	    availability: string;
	    loadPercentage: number;
	    numberOfEfficiencyCores: number;
	    numberOfPerformanceCores: number;
	
	    static createFrom(source: any = {}) {
	        return new CPUInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.deviceId = source["deviceId"];
	        this.model = source["model"];
	        this.manufacturer = source["manufacturer"];
	        this.processorType = source["processorType"];
	        this.cpuStatus = source["cpuStatus"];
	        this.numberOfCores = source["numberOfCores"];
	        this.logicalProcessors = source["logicalProcessors"];
	        this.addressWidth = source["addressWidth"];
	        this.currentClockSpeed = source["currentClockSpeed"];
	        this.maxClockSpeed = source["maxClockSpeed"];
	        this.socketDesignation = source["socketDesignation"];
	        this.availability = source["availability"];
	        this.loadPercentage = source["loadPercentage"];
	        this.numberOfEfficiencyCores = source["numberOfEfficiencyCores"];
	        this.numberOfPerformanceCores = source["numberOfPerformanceCores"];
	    }
	}
	export class Catalog {
	    generated: string;
	    count: number;
	    packagesWithIcon: number;
	    packages: AppItem[];
	
	    static createFrom(source: any = {}) {
	        return new Catalog(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.generated = source["generated"];
	        this.count = source["count"];
	        this.packagesWithIcon = source["packagesWithIcon"];
	        this.packages = this.convertValues(source["packages"], AppItem);
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
	export class DiskInfo {
	    device: string;
	    label: string;
	    fileSystem: string;
	    type: string;
	    sizeGB: number;
	    freeGB: number;
	    freeKnown: boolean;
	    bootPartition: boolean;
	    manufacturer: string;
	    model: string;
	    serial: string;
	    partitions: number;
	    description: string;
	
	    static createFrom(source: any = {}) {
	        return new DiskInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.device = source["device"];
	        this.label = source["label"];
	        this.fileSystem = source["fileSystem"];
	        this.type = source["type"];
	        this.sizeGB = source["sizeGB"];
	        this.freeGB = source["freeGB"];
	        this.freeKnown = source["freeKnown"];
	        this.bootPartition = source["bootPartition"];
	        this.manufacturer = source["manufacturer"];
	        this.model = source["model"];
	        this.serial = source["serial"];
	        this.partitions = source["partitions"];
	        this.description = source["description"];
	    }
	}
	export class GPUInfo {
	    name: string;
	    manufacturer: string;
	    driverVersion: string;
	    vramGB: number;
	    status: string;
	
	    static createFrom(source: any = {}) {
	        return new GPUInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.manufacturer = source["manufacturer"];
	        this.driverVersion = source["driverVersion"];
	        this.vramGB = source["vramGB"];
	        this.status = source["status"];
	    }
	}
	export class HardwareInfo {
	    hostname: string;
	    manufacturer: string;
	    model: string;
	    cpu: string;
	    logicalCores: number;
	    cores: number;
	    memoryGB: number;
	    motherboardManufacturer: string;
	    motherboardModel: string;
	    motherboardSerial: string;
	    biosVendor: string;
	    biosVersion: string;
	    biosReleaseDate: string;
	    biosSerial: string;
	    memoryModulesCount: number;
	
	    static createFrom(source: any = {}) {
	        return new HardwareInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.hostname = source["hostname"];
	        this.manufacturer = source["manufacturer"];
	        this.model = source["model"];
	        this.cpu = source["cpu"];
	        this.logicalCores = source["logicalCores"];
	        this.cores = source["cores"];
	        this.memoryGB = source["memoryGB"];
	        this.motherboardManufacturer = source["motherboardManufacturer"];
	        this.motherboardModel = source["motherboardModel"];
	        this.motherboardSerial = source["motherboardSerial"];
	        this.biosVendor = source["biosVendor"];
	        this.biosVersion = source["biosVersion"];
	        this.biosReleaseDate = source["biosReleaseDate"];
	        this.biosSerial = source["biosSerial"];
	        this.memoryModulesCount = source["memoryModulesCount"];
	    }
	}
	export class StartupItem {
	    name: string;
	    path: string;
	    args: string;
	    type: string;
	    source: string;
	    status: string;
	    username: string;
	
	    static createFrom(source: any = {}) {
	        return new StartupItem(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.path = source["path"];
	        this.args = source["args"];
	        this.type = source["type"];
	        this.source = source["source"];
	        this.status = source["status"];
	        this.username = source["username"];
	    }
	}
	export class SoftwareItem {
	    name: string;
	    version: string;
	    publisher: string;
	    installId: string;
	    serial: string;
	    source: string;
	
	    static createFrom(source: any = {}) {
	        return new SoftwareItem(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.version = source["version"];
	        this.publisher = source["publisher"];
	        this.installId = source["installId"];
	        this.serial = source["serial"];
	        this.source = source["source"];
	    }
	}
	export class PrinterInfo {
	    name: string;
	    driverName: string;
	    portName: string;
	    printerStatus: string;
	    isDefault: boolean;
	    isNetworkPrinter: boolean;
	    shared: boolean;
	    shareName: string;
	    location: string;
	
	    static createFrom(source: any = {}) {
	        return new PrinterInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.driverName = source["driverName"];
	        this.portName = source["portName"];
	        this.printerStatus = source["printerStatus"];
	        this.isDefault = source["isDefault"];
	        this.isNetworkPrinter = source["isNetworkPrinter"];
	        this.shared = source["shared"];
	        this.shareName = source["shareName"];
	        this.location = source["location"];
	    }
	}
	export class NetworkInfo {
	    interface: string;
	    friendlyName: string;
	    mac: string;
	    ipv4: string;
	    ipv6: string;
	    gateway: string;
	    type: string;
	    mtu: number;
	    linkSpeedMbps: number;
	    connectionStatus: string;
	    enabled: boolean;
	    physicalAdapter: boolean;
	    dhcpEnabled: boolean;
	    dnsServers: string;
	    description: string;
	    manufacturer: string;
	
	    static createFrom(source: any = {}) {
	        return new NetworkInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.interface = source["interface"];
	        this.friendlyName = source["friendlyName"];
	        this.mac = source["mac"];
	        this.ipv4 = source["ipv4"];
	        this.ipv6 = source["ipv6"];
	        this.gateway = source["gateway"];
	        this.type = source["type"];
	        this.mtu = source["mtu"];
	        this.linkSpeedMbps = source["linkSpeedMbps"];
	        this.connectionStatus = source["connectionStatus"];
	        this.enabled = source["enabled"];
	        this.physicalAdapter = source["physicalAdapter"];
	        this.dhcpEnabled = source["dhcpEnabled"];
	        this.dnsServers = source["dnsServers"];
	        this.description = source["description"];
	        this.manufacturer = source["manufacturer"];
	    }
	}
	export class MonitorInfo {
	    name: string;
	    manufacturer: string;
	    serial: string;
	    resolution: string;
	    status: string;
	
	    static createFrom(source: any = {}) {
	        return new MonitorInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.manufacturer = source["manufacturer"];
	        this.serial = source["serial"];
	        this.resolution = source["resolution"];
	        this.status = source["status"];
	    }
	}
	export class MemoryModule {
	    handle: string;
	    arrayHandle: string;
	    formFactor: string;
	    totalWidth: number;
	    dataWidth: number;
	    sizeMB: number;
	    set: number;
	    slot: string;
	    bank: string;
	    memoryTypeDetails: string;
	    maxSpeedMTs: number;
	    manufacturer: string;
	    partNumber: string;
	    serial: string;
	    assetTag: string;
	    sizeGB: number;
	    speedMHz: number;
	    type: string;
	    minVoltageMV: number;
	    maxVoltageMV: number;
	    configuredVoltageMV: number;
	
	    static createFrom(source: any = {}) {
	        return new MemoryModule(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.handle = source["handle"];
	        this.arrayHandle = source["arrayHandle"];
	        this.formFactor = source["formFactor"];
	        this.totalWidth = source["totalWidth"];
	        this.dataWidth = source["dataWidth"];
	        this.sizeMB = source["sizeMB"];
	        this.set = source["set"];
	        this.slot = source["slot"];
	        this.bank = source["bank"];
	        this.memoryTypeDetails = source["memoryTypeDetails"];
	        this.maxSpeedMTs = source["maxSpeedMTs"];
	        this.manufacturer = source["manufacturer"];
	        this.partNumber = source["partNumber"];
	        this.serial = source["serial"];
	        this.assetTag = source["assetTag"];
	        this.sizeGB = source["sizeGB"];
	        this.speedMHz = source["speedMHz"];
	        this.type = source["type"];
	        this.minVoltageMV = source["minVoltageMV"];
	        this.maxVoltageMV = source["maxVoltageMV"];
	        this.configuredVoltageMV = source["configuredVoltageMV"];
	    }
	}
	export class LoggedInUser {
	    user: string;
	    type: string;
	    tty: string;
	    host: string;
	    pid: number;
	    sid: string;
	    registry: string;
	    time: number;
	
	    static createFrom(source: any = {}) {
	        return new LoggedInUser(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.user = source["user"];
	        this.type = source["type"];
	        this.tty = source["tty"];
	        this.host = source["host"];
	        this.pid = source["pid"];
	        this.sid = source["sid"];
	        this.registry = source["registry"];
	        this.time = source["time"];
	    }
	}
	export class OperatingSystem {
	    name: string;
	    version: string;
	    build: string;
	    architecture: string;
	
	    static createFrom(source: any = {}) {
	        return new OperatingSystem(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.version = source["version"];
	        this.build = source["build"];
	        this.architecture = source["architecture"];
	    }
	}
	export class InventoryReport {
	    collectedAt: string;
	    source: string;
	    hardware: HardwareInfo;
	    os: OperatingSystem;
	    loggedInUsers: LoggedInUser[];
	    battery: BatteryInfo[];
	    bitLocker: BitLockerInfo[];
	    cpuInfo: CPUInfo[];
	    cpuFeatures: CPUFeature[];
	    memoryModules: MemoryModule[];
	    monitors: MonitorInfo[];
	    gpus: GPUInfo[];
	    volumes: DiskInfo[];
	    physicalDisks: DiskInfo[];
	    disks: DiskInfo[];
	    networks: NetworkInfo[];
	    printers: PrinterInfo[];
	    software: SoftwareItem[];
	    startupItems: StartupItem[];
	    autoexec: AutoexecItem[];
	
	    static createFrom(source: any = {}) {
	        return new InventoryReport(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.collectedAt = source["collectedAt"];
	        this.source = source["source"];
	        this.hardware = this.convertValues(source["hardware"], HardwareInfo);
	        this.os = this.convertValues(source["os"], OperatingSystem);
	        this.loggedInUsers = this.convertValues(source["loggedInUsers"], LoggedInUser);
	        this.battery = this.convertValues(source["battery"], BatteryInfo);
	        this.bitLocker = this.convertValues(source["bitLocker"], BitLockerInfo);
	        this.cpuInfo = this.convertValues(source["cpuInfo"], CPUInfo);
	        this.cpuFeatures = this.convertValues(source["cpuFeatures"], CPUFeature);
	        this.memoryModules = this.convertValues(source["memoryModules"], MemoryModule);
	        this.monitors = this.convertValues(source["monitors"], MonitorInfo);
	        this.gpus = this.convertValues(source["gpus"], GPUInfo);
	        this.volumes = this.convertValues(source["volumes"], DiskInfo);
	        this.physicalDisks = this.convertValues(source["physicalDisks"], DiskInfo);
	        this.disks = this.convertValues(source["disks"], DiskInfo);
	        this.networks = this.convertValues(source["networks"], NetworkInfo);
	        this.printers = this.convertValues(source["printers"], PrinterInfo);
	        this.software = this.convertValues(source["software"], SoftwareItem);
	        this.startupItems = this.convertValues(source["startupItems"], StartupItem);
	        this.autoexec = this.convertValues(source["autoexec"], AutoexecItem);
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
	
	
	
	
	
	export class OsqueryStatus {
	    installed: boolean;
	    path: string;
	    suggestedPackageID: string;
	
	    static createFrom(source: any = {}) {
	        return new OsqueryStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.installed = source["installed"];
	        this.path = source["path"];
	        this.suggestedPackageID = source["suggestedPackageID"];
	    }
	}
	
	
	
	export class UpgradeItem {
	    name: string;
	    id: string;
	    currentVersion: string;
	    availableVersion: string;
	    source: string;
	
	    static createFrom(source: any = {}) {
	        return new UpgradeItem(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.id = source["id"];
	        this.currentVersion = source["currentVersion"];
	        this.availableVersion = source["availableVersion"];
	        this.source = source["source"];
	    }
	}

}

