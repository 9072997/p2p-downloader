set baseUrl to "http://localhost:1817/status/"
set numParts to 1
set partsComplete to 0
set origionalPackageName to do shell script "curl " & quoted form of baseUrl & "name"
set packageName to origionalPackageName
repeat until partsComplete is equal to numParts or packageName is not equal to origionalPackageName
	set packageName to do shell script "curl " & quoted form of baseUrl & "name"
	set numParts to do shell script "curl " & quoted form of baseUrl & "numParts"
	set partsComplete to do shell script "curl " & quoted form of baseUrl & "partsComplete"
	
	-- Update the initial progress information
	set progress total steps to numParts
	set progress completed steps to partsComplete
	set progress description to "Downloading " & packageName
	set progress additional description to "Part " & partsComplete & " of " & numParts
	
	delay (0.25)
end repeat
