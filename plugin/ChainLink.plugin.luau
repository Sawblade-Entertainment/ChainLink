--!strict
-- ChainLink Studio sync plugin

local HttpService = game:GetService("HttpService")
local ScriptEditorService = game:GetService("ScriptEditorService")
local ChangeHistoryService = game:GetService("ChangeHistoryService")

local ATTR = "ChainLinkPath"
local SETTING_URL = "ChainLink_Url"
local SETTING_TOKEN = "ChainLink_Token"
local DEFAULT_URL = "http://localhost:8080"

-- ---------- State ----------

local running = false
local lastSeq = 0
local sessionId = ""
local instanceCache: { [string]: Instance } = {}

-- ---------- UI ----------

local toolbar = plugin:CreateToolbar("ChainLink")
local toggleButton = toolbar:CreateButton("ChainLink", "Open ChainLink sync panel", "rbxassetid://4458901886")

local widget = plugin:CreateDockWidgetPluginGui("ChainLinkWidget", DockWidgetPluginGuiInfo.new(
	Enum.InitialDockState.Right, false, false, 300, 400, 220, 200
))
widget.Title = "ChainLink"

local function make(class: string, props: { [string]: any }, parent: Instance?): Instance
	local inst = Instance.new(class)
	for k, v in props do (inst :: any)[k] = v end
	inst.Parent = parent
	return inst
end

local root = make("Frame", { Size = UDim2.fromScale(1, 1), BackgroundColor3 = Color3.fromRGB(46, 46, 46), BorderSizePixel = 0 }, widget)
make("UIPadding", { PaddingTop = UDim.new(0, 8), PaddingBottom = UDim.new(0, 8), PaddingLeft = UDim.new(0, 8), PaddingRight = UDim.new(0, 8) }, root)
make("UIListLayout", { Padding = UDim.new(0, 6), SortOrder = Enum.SortOrder.LayoutOrder }, root)

local statusRow = make("Frame", { Size = UDim2.new(1, 0, 0, 20), BackgroundTransparency = 1, LayoutOrder = 1 }, root)
local statusDot = make("Frame", { Size = UDim2.fromOffset(12, 12), Position = UDim2.fromOffset(0, 4), BackgroundColor3 = Color3.fromRGB(200, 60, 60), BorderSizePixel = 0 }, statusRow)
make("UICorner", { CornerRadius = UDim.new(1, 0) }, statusDot)
local statusLabel = make("TextLabel", {
	Size = UDim2.new(1, -20, 1, 0), Position = UDim2.fromOffset(20, 0), BackgroundTransparency = 1,
	Text = "Disconnected", TextColor3 = Color3.fromRGB(220, 220, 220), TextXAlignment = Enum.TextXAlignment.Left, Font = Enum.Font.SourceSans, TextSize = 16,
}, statusRow) :: TextLabel

local function textBox(placeholder: string, value: string, order: number): TextBox
	return make("TextBox", {
		Size = UDim2.new(1, 0, 0, 26), BackgroundColor3 = Color3.fromRGB(30, 30, 30), BorderSizePixel = 0,
		Text = value, PlaceholderText = placeholder, TextColor3 = Color3.fromRGB(230, 230, 230),
		ClearTextOnFocus = false, Font = Enum.Font.Code, TextSize = 14, TextXAlignment = Enum.TextXAlignment.Left, LayoutOrder = order,
	}, root) :: TextBox
end

local urlBox = textBox("Server URL", plugin:GetSetting(SETTING_URL) or DEFAULT_URL, 2)
local tokenBox = textBox("Token (optional)", plugin:GetSetting(SETTING_TOKEN) or "", 3)

local connectButton = make("TextButton", {
	Size = UDim2.new(1, 0, 0, 28), BackgroundColor3 = Color3.fromRGB(0, 120, 215), BorderSizePixel = 0,
	Text = "Connect", TextColor3 = Color3.new(1, 1, 1), Font = Enum.Font.SourceSansBold, TextSize = 16, LayoutOrder = 4,
}, root) :: TextButton

local logFrame = make("ScrollingFrame", {
	Size = UDim2.new(1, 0, 1, -130), BackgroundColor3 = Color3.fromRGB(30, 30, 30), BorderSizePixel = 0,
	CanvasSize = UDim2.new(), AutomaticCanvasSize = Enum.AutomaticSize.Y, ScrollBarThickness = 6, LayoutOrder = 5,
}, root) :: ScrollingFrame
make("UIListLayout", { SortOrder = Enum.SortOrder.LayoutOrder }, logFrame)

local logCount = 0
local function logLine(msg: string, color: Color3?)
	logCount += 1
	make("TextLabel", {
		Size = UDim2.new(1, -6, 0, 16), BackgroundTransparency = 1, Text = msg, LayoutOrder = logCount,
		TextColor3 = color or Color3.fromRGB(200, 200, 200), Font = Enum.Font.Code, TextSize = 13,
		TextXAlignment = Enum.TextXAlignment.Left, TextTruncate = Enum.TextTruncate.AtEnd,
	}, logFrame)
	if logCount > 200 then
		local children = logFrame:GetChildren()
		for _, c in children do
			if c:IsA("TextLabel") then c:Destroy() break end
		end
	end
	logFrame.CanvasPosition = Vector2.new(0, math.huge)
	print("[ChainLink] " .. msg)
end

local function setStatus(connected: boolean, text: string)
	statusDot.BackgroundColor3 = connected and Color3.fromRGB(60, 180, 90) or Color3.fromRGB(200, 60, 60)
	statusLabel.Text = text
	connectButton.Text = running and "Disconnect" or "Connect"
	connectButton.BackgroundColor3 = running and Color3.fromRGB(160, 50, 50) or Color3.fromRGB(0, 120, 215)
end

-- ---------- Instance resolution ----------

local function resolveTarget(target: string): Instance?
	local parts = string.split(target, ".")
	local ok, current = pcall(function() return game:GetService(parts[1]) end)
	if not ok or not current then return nil end
	for i = 2, #parts do
		local child = (current :: Instance):FindFirstChild(parts[i])
		if not child then
			-- Create missing target folders automatically
			child = Instance.new("Folder")
			child.Name = parts[i]
			child.Parent = current
		end
		current = child
	end
	return current
end

local function keyFor(target: string, segments: { string }): string
	return target .. "/" .. table.concat(segments, "/")
end

local function findInstance(target: string, segments: { string }): Instance?
	local key = keyFor(target, segments)
	local cached = instanceCache[key]
	if cached and cached.Parent then return cached end
	instanceCache[key] = nil

	local current = resolveTarget(target)
	if not current then return nil end
	for _, seg in segments do
		current = (current :: Instance):FindFirstChild(seg)
		if not current then return nil end
	end
	instanceCache[key] = current
	return current
end

-- Ensures Folders exist along `segments` (not including the final element) and returns the parent.
local function ensureParent(target: string, segments: { string }): Instance?
	local current = resolveTarget(target)
	if not current then return nil end
	for i = 1, #segments - 1 do
		local seg = segments[i]
		local child = (current :: Instance):FindFirstChild(seg)
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

local function replaceInstance(old: Instance, newClass: string): Instance
	local new = Instance.new(newClass)
	new.Name = old.Name
	for attr, value in old:GetAttributes() do
		new:SetAttribute(attr, value)
	end
	for _, child in old:GetChildren() do child.Parent = new end
	new.Parent = old.Parent
	old:Destroy()
	return new
end

local function setSource(inst: Instance, source: string)
	if inst:IsA("LuaSourceContainer") then
		local ok, err = pcall(function()
			ScriptEditorService:UpdateSourceAsync(inst, function() return source end)
		end)
		if not ok then
			(inst :: any).Source = source -- fallback (script not open in editor)
		end
	elseif inst:IsA("StringValue") then
		inst.Value = source
	end
end

-- ---------- Applying events ----------

local function applyCreateOrUpdate(ev)
	local segments: { string } = ev.instancePath
	local key = keyFor(ev.target, segments)
	local inst: Instance?

	if #segments == 0 then
		-- init file directly in a root dir: the root itself can't be a script; ignore.
		return
	end

	local parent = ensureParent(ev.target, segments)
	if not parent then
		logLine("Target not found: " .. ev.target, Color3.fromRGB(230, 120, 60))
		return
	end

	local name = segments[#segments]
	inst = parent:FindFirstChild(name)
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
	if not inst then return end
	
	-- Filter: Never delete the root target itself (e.g. ServerScriptService)
	if #ev.instancePath == 0 then
		logLine("Skipping delete of root target: " .. ev.target, Color3.fromRGB(230, 180, 60))
		return
	end

	if inst:GetAttribute(ATTR) ~= key then
		logLine("Skipping delete of unmanaged " .. inst:GetFullName(), Color3.fromRGB(230, 180, 60))
		return
	end
	if ev.keepChildren and #inst:GetChildren() > 0 then
		instanceCache[key] = replaceInstance(inst, "Folder")
	else
		inst:Destroy()
	end
end

local function pruneStale(validKeys: { [string]: boolean })
	-- Remove managed instances that are no longer present in the snapshot.
	for _, inst in game:GetDescendants() do
		local key = inst:GetAttribute(ATTR)
		if type(key) == "string" and not validKeys[key] then
			-- Keep folders that still have managed descendants (snapshot only lists files).
			local keepAsFolder = false
			if inst:IsA("Folder") then
				for _, d in inst:GetDescendants() do
					local dk = d:GetAttribute(ATTR)
					if type(dk) == "string" and validKeys[dk] then keepAsFolder = true break end
				end
			end
			if not keepAsFolder then inst:Destroy() end
		end
	end
	instanceCache = {}
end

local function applyBatch(resp)
	local events = resp.events or {}
	if #events == 0 and not resp.full then return end

	local recording = ChangeHistoryService:TryBeginRecording("ChainLink sync")
	local ok, err = pcall(function()
		if resp.full then
			local valid: { [string]: boolean } = {}
			for _, ev in events do
				valid[keyFor(ev.target, ev.instancePath)] = true
				-- mark folder ancestors valid too
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
			logLine(string.format("Snapshot applied: %d files", #events), Color3.fromRGB(120, 200, 140))
		end
	end)
	if recording then
		ChangeHistoryService:FinishRecording(recording, ok and Enum.FinishRecordingOperation.Commit or Enum.FinishRecordingOperation.Cancel)
	end
	if not ok then
		logLine("Apply error: " .. tostring(err), Color3.fromRGB(230, 80, 80))
	end
end

-- ---------- Polling ----------

local function request(url: string, token: string)
	local headers = {}
	if token ~= "" then headers["X-ChainLink-Token"] = token end
	local res = HttpService:RequestAsync({ Url = url, Method = "GET", Headers = headers })
	if not res.Success then
		error(string.format("HTTP %d %s", res.StatusCode, res.StatusMessage))
	end
	return HttpService:JSONDecode(res.Body)
end

local function pollLoop(baseUrl: string, token: string)
	local backoff = 1
	local fatal = false
	while running do
		local url = string.format("%s/changes?since=%d&session=%s", baseUrl, lastSeq, HttpService:UrlEncode(sessionId))
		local ok, result = pcall(request, url, token)
		if not running then break end

		if ok then
			if backoff > 1 then logLine("Reconnected", Color3.fromRGB(120, 200, 140)) end
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
				logLine("Enable HTTP requests: Game Settings > Security > Allow HTTP Requests", Color3.fromRGB(230, 80, 80))
				running = false
				fatal = true
				break
			elseif string.find(msg, "requests exceeded limit", 1, true) then
				logLine("Roblox HTTP limit reached. Backing off...", Color3.fromRGB(230, 180, 60))
				-- Increase backoff more aggressively for rate limits
				backoff = math.max(backoff * 2, 5)
				setStatus(false, "Rate limited... backing off")
				task.wait(backoff)
				continue
			end
			setStatus(false, "Reconnecting… (" .. msg .. ")")
			if backoff == 1 then logLine("Connection lost: " .. msg, Color3.fromRGB(230, 120, 60)) end
			task.wait(backoff)
			backoff = math.min(backoff * 2, 10)
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
	logLine("Disconnected")
end

connectButton.MouseButton1Click:Connect(function()
	if running then disconnect() else connect() end
end)

toggleButton.Click:Connect(function()
	widget.Enabled = not widget.Enabled
end)

widget:GetPropertyChangedSignal("Enabled"):Connect(function()
	toggleButton:SetActive(widget.Enabled)
end)

-- Stop the poll loop cleanly on plugin reload so we don't end up with duplicate pollers.
plugin.Unloading:Connect(function()
	running = false
end)

setStatus(false, "Disconnected")
logLine("ChainLink ready. Run `chainlink serve` and press Connect.")
