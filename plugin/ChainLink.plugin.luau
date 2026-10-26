--!strict
--!native
--!optimize 2
-- ChainLink Studio sync plugin

local HttpService = game:GetService("HttpService")
local ScriptEditorService = game:GetService("ScriptEditorService")
local ChangeHistoryService = game:GetService("ChangeHistoryService")

local ATTR = "ChainLinkPath"
local SETTING_URL = "ChainLink_Url"
local SETTING_TOKEN = "ChainLink_Token"
local SETTING_LOGS = "ChainLink_ShowLogs"
local DEFAULT_URL = "http://localhost:8080"

-- ---------- State ----------

local running = false
local lastSeq = 0
local sessionId = ""
local instanceCache = {}

-- ---------- Theme ----------

local Studio = settings().Studio
local SGC = Enum.StudioStyleGuideColor
local SGM = Enum.StudioStyleGuideModifier

local themeBindings: { () -> () } = {}

local function themed(fn: () -> ())
    table.insert(themeBindings, fn)
    fn()
end

local function color(c: Enum.StudioStyleGuideColor, m: Enum.StudioStyleGuideModifier?): Color3
    return Studio.Theme:GetColor(c, m or SGM.Default)
end

Studio.ThemeChanged:Connect(function()
    for _, fn in themeBindings do
        fn()
    end
end)

-- ---------- UI ----------

local toolbar = plugin:CreateToolbar("ChainLink")
local toggleButton = toolbar:CreateButton("ChainLink", "Open ChainLink sync panel", "rbxassetid://4458901886")
toggleButton.Icon = "rbxassetid://121842935231412"

local widget = plugin:CreateDockWidgetPluginGuiAsync(
    "ChainLinkWidget",
    DockWidgetPluginGuiInfo.new(Enum.InitialDockState.Right, false, false, 300, 400, 220, 180)
)
widget.Title = "ChainLink"

local function make(class: string, props: { [string]: any }, parent: Instance?): any
    local inst = Instance.new(class)
    for k, v in props do
        (inst :: any)[k] = v
    end
    inst.Parent = parent
    return inst
end

local function addBorder(inst: GuiObject, colorKey: Enum.StudioStyleGuideColor)
    local stroke = make("UIStroke", { Thickness = 1, ApplyStrokeMode = Enum.ApplyStrokeMode.Border }, inst)
    themed(function()
        stroke.Color = color(colorKey)
    end)
    return stroke
end

local root = make("Frame", { Size = UDim2.fromScale(1, 1), BorderSizePixel = 0 }, widget)
themed(function()
    root.BackgroundColor3 = color(SGC.MainBackground)
end)
make("UIPadding", {
    PaddingTop = UDim.new(0, 8),
    PaddingBottom = UDim.new(0, 8),
    PaddingLeft = UDim.new(0, 8),
    PaddingRight = UDim.new(0, 8),
}, root)
make("UIListLayout", { Padding = UDim.new(0, 6), SortOrder = Enum.SortOrder.LayoutOrder }, root)

-- Status row
local statusRow = make("Frame", { Size = UDim2.new(1, 0, 0, 20), BackgroundTransparency = 1, LayoutOrder = 1 }, root)
local statusDot = make("Frame", {
    Size = UDim2.fromOffset(8, 8),
    Position = UDim2.fromOffset(2, 6),
    BorderSizePixel = 0,
}, statusRow)
make("UICorner", { CornerRadius = UDim.new(1, 0) }, statusDot)
local statusLabel = make("TextLabel", {
    Size = UDim2.new(1, -16, 1, 0),
    Position = UDim2.fromOffset(16, 0),
    BackgroundTransparency = 1,
    Text = "Disconnected",
    TextXAlignment = Enum.TextXAlignment.Left,
    Font = Enum.Font.SourceSans,
    TextSize = 14,
    TextTruncate = Enum.TextTruncate.AtEnd,
}, statusRow)
themed(function()
    statusLabel.TextColor3 = color(SGC.MainText)
end)

-- Labeled text field (Studio properties-panel style)
local function field(labelText: string, placeholder: string, value: string, order: number): TextBox
    local row = make("Frame", { Size = UDim2.new(1, 0, 0, 40), BackgroundTransparency = 1, LayoutOrder = order }, root)
    local label = make("TextLabel", {
        Size = UDim2.new(1, 0, 0, 16),
        BackgroundTransparency = 1,
        Text = labelText,
        Font = Enum.Font.SourceSans,
        TextSize = 14,
        TextXAlignment = Enum.TextXAlignment.Left,
    }, row)
    local box = make("TextBox", {
        Size = UDim2.new(1, 0, 0, 22),
        Position = UDim2.fromOffset(0, 18),
        BorderSizePixel = 0,
        Text = value,
        PlaceholderText = placeholder,
        ClearTextOnFocus = false,
        Font = Enum.Font.SourceSans,
        TextSize = 14,
        TextXAlignment = Enum.TextXAlignment.Left,
    }, row)
    make("UIPadding", { PaddingLeft = UDim.new(0, 6), PaddingRight = UDim.new(0, 6) }, box)
    local stroke = addBorder(box, SGC.InputFieldBorder)

    local function restyle()
        local focused = box:IsFocused()
        label.TextColor3 = color(SGC.MainText)
        box.BackgroundColor3 = color(SGC.InputFieldBackground)
        box.TextColor3 = color(SGC.MainText)
        box.PlaceholderColor3 = color(SGC.DimmedText)
        stroke.Color = color(SGC.InputFieldBorder, focused and SGM.Selected or SGM.Default)
    end
    themed(restyle)
    box.Focused:Connect(restyle)
    box.FocusLost:Connect(restyle)
    return box
end

local urlBox = field("Server URL", "http://localhost:8080", plugin:GetSetting(SETTING_URL) or DEFAULT_URL, 2)
local tokenBox = field("Token", "optional", plugin:GetSetting(SETTING_TOKEN) or "", 3)

-- Connect button
local connectButton = make("TextButton", {
    Size = UDim2.new(1, 0, 0, 26),
    BorderSizePixel = 0,
    Text = "Connect",
    Font = Enum.Font.SourceSans,
    TextSize = 14,
    AutoButtonColor = false,
    LayoutOrder = 4,
}, root)
make("UICorner", { CornerRadius = UDim.new(0, 3) }, connectButton)
local connectStroke = addBorder(connectButton, SGC.ButtonBorder)
local connectHover = false

local function styleConnect()
    local mod = connectHover and SGM.Hover or SGM.Default
    if running then
        connectButton.BackgroundColor3 = color(SGC.Button, mod)
        connectButton.TextColor3 = color(SGC.ButtonText, mod)
        connectStroke.Color = color(SGC.ButtonBorder, mod)
    else
        connectButton.BackgroundColor3 = color(SGC.DialogMainButton, mod)
        connectButton.TextColor3 = color(SGC.DialogMainButtonText, mod)
        connectStroke.Color = color(SGC.DialogMainButton, mod)
    end
end
themed(styleConnect)
connectButton.MouseEnter:Connect(function()
    connectHover = true
    styleConnect()
end)
connectButton.MouseLeave:Connect(function()
    connectHover = false
    styleConnect()
end)

-- Divider
local divider = make("Frame", { Size = UDim2.new(1, 0, 0, 1), BorderSizePixel = 0, LayoutOrder = 5 }, root)
themed(function()
    divider.BackgroundColor3 = color(SGC.Border)
end)

-- Collapsible "Output" section (like the Properties panel headers)
local showLogs = plugin:GetSetting(SETTING_LOGS) == true

local logHeader = make("TextButton", {
    Size = UDim2.new(1, 0, 0, 20),
    BorderSizePixel = 0,
    Text = "",
    AutoButtonColor = false,
    LayoutOrder = 6,
}, root)
local logArrow = make("TextLabel", {
    Size = UDim2.fromOffset(16, 20),
    BackgroundTransparency = 1,
    Font = Enum.Font.SourceSans,
    TextSize = 14,
}, logHeader)
local logTitle = make("TextLabel", {
    Size = UDim2.new(1, -16, 1, 0),
    Position = UDim2.fromOffset(16, 0),
    BackgroundTransparency = 1,
    Text = "Output",
    Font = Enum.Font.SourceSansSemibold,
    TextSize = 14,
    TextXAlignment = Enum.TextXAlignment.Left,
}, logHeader)

local logFrame = make("ScrollingFrame", {
    Size = UDim2.new(1, 0, 0, 0),
    BorderSizePixel = 0,
    CanvasSize = UDim2.new(),
    AutomaticCanvasSize = Enum.AutomaticSize.Y,
    ScrollBarThickness = 8,
    ScrollBarImageTransparency = 0.4,
    LayoutOrder = 7,
    Visible = false,
}, root)
make("UIFlexItem", { FlexMode = Enum.UIFlexMode.Fill }, logFrame)
make("UIListLayout", { SortOrder = Enum.SortOrder.LayoutOrder }, logFrame)
make("UIPadding", { PaddingLeft = UDim.new(0, 4), PaddingTop = UDim.new(0, 2) }, logFrame)
addBorder(logFrame, SGC.Border)

local logHover = false
local function styleLogSection()
    logHeader.BackgroundColor3 = color(SGC.CategoryItem, logHover and SGM.Hover or SGM.Default)
    logArrow.TextColor3 = color(SGC.MainText)
    logTitle.TextColor3 = color(SGC.MainText)
    logArrow.Text = showLogs and "-" or "+"
    logFrame.Visible = showLogs
    logFrame.BackgroundColor3 = color(SGC.ScrollBarBackground)
    logFrame.ScrollBarImageColor3 = color(SGC.ScrollBar)
end
themed(styleLogSection)
logHeader.MouseEnter:Connect(function()
    logHover = true
    styleLogSection()
end)
logHeader.MouseLeave:Connect(function()
    logHover = false
    styleLogSection()
end)
logHeader.MouseButton1Click:Connect(function()
    showLogs = not showLogs
    plugin:SetSetting(SETTING_LOGS, showLogs)
    styleLogSection()
end)

-- ---------- Logging ----------

type LogLevel = "info" | "ok" | "warn" | "error" | "dim"

local function levelColor(level: LogLevel): Color3
    if level == "ok" then
        return color(SGC.InfoText)
    elseif level == "warn" then
        return color(SGC.WarningText)
    elseif level == "error" then
        return color(SGC.ErrorText)
    elseif level == "dim" then
        return color(SGC.DimmedText)
    end
    return color(SGC.MainText)
end

local logCount = 0
local function logLine(msg: string, level: LogLevel?)
    logCount += 1
    local lvl: LogLevel = level or "info"
    local line = make("TextLabel", {
        Size = UDim2.new(1, -8, 0, 16),
        BackgroundTransparency = 1,
        Text = msg,
        LayoutOrder = logCount,
        Font = Enum.Font.Code,
        TextSize = 13,
        TextXAlignment = Enum.TextXAlignment.Left,
        TextTruncate = Enum.TextTruncate.AtEnd,
    }, logFrame)
    themed(function()
        if line.Parent then
            line.TextColor3 = levelColor(lvl)
        end
    end)
    if logCount > 200 then
        for _, c in logFrame:GetChildren() do
            if c:IsA("TextLabel") then
                c:Destroy()
                break
            end
        end
    end
    if showLogs then
        logFrame.CanvasPosition = Vector2.new(0, math.huge)
    end
end

local function setStatus(connected: boolean, text: string)
    statusDot.BackgroundColor3 = connected and Color3.fromRGB(60, 180, 90) or color(SGC.ErrorText)
    statusLabel.Text = text
    connectButton.Text = running and "Disconnect" or "Connect"
    styleConnect()
end

-- ---------- Instance resolution ----------

local function resolveTarget(target)
    local parts = string.split(target, ".")
    local ok, current = pcall(function()
        return game:GetService(parts[1])
    end)
    if not ok or not current then
        return nil
    end
    for i = 2, #parts do
        local child = current:FindFirstChild(parts[i])
        if not child then
            child = Instance.new("Folder")
            child.Name = parts[i]
            child.Parent = current
        end
        current = child
    end
    return current
end

local function keyFor(target, segments)
    return target .. "/" .. table.concat(segments, "/")
end

local function findInstance(target, segments)
    local key = keyFor(target, segments)
    local cached = instanceCache[key]
    if cached and cached.Parent then
        return cached
    end
    instanceCache[key] = nil

    local current = resolveTarget(target)
    if not current then
        return nil
    end
    for _, seg in segments do
        current = current:FindFirstChild(seg)
        if not current then
            return nil
        end
    end
    instanceCache[key] = current
    return current
end

local function ensureParent(target, segments)
    local current = resolveTarget(target)
    if not current then
        return nil
    end
    for i = 1, #segments - 1 do
        local seg = segments[i]
        local child = current:FindFirstChild(seg)
        if not child then
            child = Instance.new("Folder")
            child.Name = seg
            child:SetAttribute(ATTR, keyFor(target, { table.unpack(segments, 1, i) }))
            child.Parent = current
        end
        instanceCache[keyFor(target, { table.unpack(segments, 1, i) })] = child
        current = child
    end
    return current
end

local function replaceInstance(old, newClass)
    local new = Instance.new(newClass)
    new.Name = old.Name
    for attr, value in old:GetAttributes() do
        new:SetAttribute(attr, value)
    end
    for _, child in old:GetChildren() do
        child.Parent = new
    end
    new.Parent = old.Parent
    old:Destroy()
    return new
end

local function setSource(inst, source)
    if inst:IsA("LuaSourceContainer") then
        local ok = pcall(function()
            ScriptEditorService:UpdateSourceAsync(inst, function()
                return source
            end)
        end)
        if not ok then
            inst.Source = source
        end
    elseif inst:IsA("StringValue") then
        inst.Value = source
    end
end

-- ---------- Applying events ----------

local function applyCreateOrUpdate(ev)
    local segments = ev.instancePath
    local key = keyFor(ev.target, segments)

    if #segments == 0 then
        return
    end

    local parent = ensureParent(ev.target, segments)
    if not parent then
        logLine("Target not found: " .. ev.target, "warn")
        return
    end

    local name = segments[#segments]
    local inst = parent:FindFirstChild(name)
    if inst and inst.ClassName ~= ev.className then
        inst = replaceInstance(inst, ev.className)
    elseif not inst then
        inst = Instance.new(ev.className)
        inst.Name = name
        inst.Parent = parent
    end
    inst:SetAttribute(ATTR, key)
    instanceCache[key] = inst
    setSource(inst, ev.source or "")
end

local function applyDelete(ev)
    local key = keyFor(ev.target, ev.instancePath)
    local inst = findInstance(ev.target, ev.instancePath)
    instanceCache[key] = nil
    if not inst then
        return
    end

    if #ev.instancePath == 0 then
        logLine("Skipping delete of root target: " .. ev.target, "warn")
        return
    end

    if inst:GetAttribute(ATTR) ~= key then
        logLine("Skipping delete of unmanaged " .. inst:GetFullName(), "warn")
        return
    end
    if ev.keepChildren and #inst:GetChildren() > 0 then
        instanceCache[key] = replaceInstance(inst, "Folder")
    else
        inst:Destroy()
    end
end

local function pruneStale(validKeys)
    for _, inst in game:GetDescendants() do
        local key = inst:GetAttribute(ATTR)
        if type(key) == "string" and not validKeys[key] then
            local keepAsFolder = false
            if inst:IsA("Folder") then
                for _, d in inst:GetDescendants() do
                    local dk = d:GetAttribute(ATTR)
                    if type(dk) == "string" and validKeys[dk] then
                        keepAsFolder = true
                        break
                    end
                end
            end
            if not keepAsFolder then
                inst:Destroy()
            end
        end
    end
    instanceCache = {}
end

local function applyBatch(resp)
    local events = resp.events or {}
    if #events == 0 and not resp.full then
        return
    end

    local recording = ChangeHistoryService:TryBeginRecording("ChainLink sync")
    local ok, err = pcall(function()
        if resp.full then
            local valid = {}
            for _, ev in events do
                valid[keyFor(ev.target, ev.instancePath)] = true
                for i = 1, #ev.instancePath - 1 do
                    valid[keyFor(ev.target, { table.unpack(ev.instancePath, 1, i) })] = true
                end
            end
            pruneStale(valid)
        end
        for _, ev in events do
            if ev.op == "DELETE" then
                applyDelete(ev)
            else
                applyCreateOrUpdate(ev)
            end
            if not resp.full then
                logLine(string.format("[%s] %s", ev.op, ev.path))
            end
        end
        if resp.full then
            logLine(string.format("Snapshot applied: %d files", #events), "ok")
        end
    end)
    if recording then
        ChangeHistoryService:FinishRecording(
            recording,
            ok and Enum.FinishRecordingOperation.Commit or Enum.FinishRecordingOperation.Cancel
        )
    end
    if not ok then
        logLine("Apply error: " .. tostring(err), "error")
    end
end

-- ---------- Polling ----------

local function request(url, token)
    local headers = {}
    if token ~= "" then
        headers["X-ChainLink-Token"] = token
    end
    local res = HttpService:RequestAsync({ Url = url, Method = "GET", Headers = headers })
    if not res.Success then
        error(string.format("HTTP %d %s", res.StatusCode, res.StatusMessage))
    end
    return HttpService:JSONDecode(res.Body)
end

local function pollLoop(baseUrl, token)
    local backoff = 1
    local fatal = false
    local skip = false
    while running do
        local url = string.format("%s/changes?since=%d&session=%s", baseUrl, lastSeq, HttpService:UrlEncode(sessionId))
        local ok, result = pcall(request, url, token)
        if not running then
            break
        end

        if ok then
            if backoff > 1 then
                logLine("Reconnected", "ok")
            end
            backoff = 1
            setStatus(true, "Connected (" .. baseUrl .. ")")

            if result.sessionId ~= sessionId then
                sessionId = result.sessionId
            end

            applyBatch(result)
            lastSeq = result.seq
        else
            local msg = tostring(result)
            if string.find(msg, "not enabled", 1, true) then
                logLine("Enable HTTP requests: Game Settings > Security > Allow HTTP Requests", "error")
                running = false
                fatal = true
                break
            elseif string.find(msg, "requests exceeded limit", 1, true) then
                logLine("Roblox HTTP limit reached. Backing off...", "warn")
                backoff = math.max(backoff * 2, 5)
                setStatus(false, "Rate limited... backing off")
                task.wait(backoff)
                skip = true
            end
            if not skip then
                setStatus(false, "Reconnecting… (" .. msg .. ")")
                if backoff == 1 then
                    logLine("Connection lost: " .. msg, "warn")
                end
                task.wait(backoff)
                backoff = math.min(backoff * 2, 10)
            end
            skip = false
        end
    end
    setStatus(false, fatal and "HTTP disabled" or "Disconnected")
end

local function connect()
    local baseUrl = string.gsub(urlBox.Text, "/+$", "")
    local token = tokenBox.Text
    plugin:SetSetting(SETTING_URL, baseUrl)
    plugin:SetSetting(SETTING_TOKEN, token)

    running = true
    lastSeq = 0
    sessionId = ""
    setStatus(false, "Connecting…")
    logLine("Connecting to " .. baseUrl)
    task.spawn(pollLoop, baseUrl, token)
end

local function disconnect()
    running = false
    setStatus(false, "Disconnected")
    logLine("Disconnected", "dim")
end

connectButton.MouseButton1Click:Connect(function()
    if running then
        disconnect()
    else
        connect()
    end
end)

toggleButton.Click:Connect(function()
    widget.Enabled = not widget.Enabled
end)

widget:GetPropertyChangedSignal("Enabled"):Connect(function()
    toggleButton:SetActive(widget.Enabled)
end)

plugin.Unloading:Connect(function()
    running = false
end)

setStatus(false, "Disconnected")
logLine("ChainLink ready. Run `chainlink serve` and press Connect.", "dim")