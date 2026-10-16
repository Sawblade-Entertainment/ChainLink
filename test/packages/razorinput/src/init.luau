--!strict
--!native
--!optimize 2

local InputController = {}

local InputBinds: { [Enum.KeyCode]: any } = {}

function InputController.createAxis(up: string, down: string, left: string, right: string): { string }
	return { up, down, left, right }
end

function InputController:bindAxis(action: any, ...: { string } | string)
	for _, event in pairs(table.pack(...)) do
		if type(event) == "table" then
			local axisDir = { 0, 0, 0, 0 } -- UP, DOWN, LEFT, RIGHT

			for i = 1, 4 do
				local code = Enum.KeyCode:FromName(event[i])

				if not code then
					warn(event[i] .. " is not a valid KeyCode name")

					continue
				end

				InputBinds[code] = function(pressed: boolean)
					axisDir[i] = (pressed and 1) or 0

					action(pressed, Vector3.new(axisDir[4] - axisDir[3], axisDir[1] - axisDir[2]))
				end
			end
		elseif type(event) == "string" then
			local code = Enum.KeyCode:FromName(event)

			if not code then
				warn(event .. " is not a valid KeyCode name")

				continue
			end

			InputBinds[code] = action
		end
	end
end

function InputController:bindAction(action: any, ...: string)
	for _, event in pairs(table.pack(...)) do
		if type(event) == "string" then
			local code = Enum.KeyCode:FromName(event)

			if not code then
				warn(event .. " is not a valid KeyCode name")

				return
			end

			InputBinds[code] = action
		end
	end
end

function InputController:input(inp: InputObject)
	if InputBinds[inp.KeyCode] then
		InputBinds[inp.KeyCode](inp.UserInputState ~= Enum.UserInputState.End, inp.Position)
	end
end

return InputController
