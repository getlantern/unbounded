function TeamButton() {
	const handleClick = () => {
		localStorage.removeItem('team_code');
		window.location.reload();
	};
  
	return (
	  <button onClick={handleClick}>Reset Team Code</button>
	);
  }
  export default TeamButton;