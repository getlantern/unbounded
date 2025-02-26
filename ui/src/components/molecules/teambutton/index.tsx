
const TeamPop = () => {
	//does the popup until there is some code set...
	let browserTeamCode = localStorage.getItem("team_code")
	return (
		<>
			Code: {browserTeamCode}
		</>
	)
}

export default TeamPop